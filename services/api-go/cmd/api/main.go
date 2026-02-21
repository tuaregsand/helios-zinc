package main

import (
	"bufio"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

//go:embed web/index.html openapi.yaml
var apiAssetsFS embed.FS

type Server struct {
	db *pgxpool.Pool
}

type APIConfig struct {
	Addr                string
	ReadTimeout         time.Duration
	ReadHeaderTimeout   time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	RequestTimeout      time.Duration
	ReadyTimeout        time.Duration
	MaxBodyBytes        int64
	RateLimitRPS        float64
	RateLimitBurst      int
	RateLimitStore      string
	RateLimitWindow     time.Duration
	RateLimitFailOpen   bool
	RateLimitRedisAddr  string
	RateLimitRedisDB    int
	RateLimitRedisPass  string
	RateLimitRedisTLS   bool
	RateLimitRedisKey   string
	APIKeys             []string
	CORSAllowOrigins    map[string]struct{}
	OpenAPIToken        string
	JWTJWKSURL          string
	JWTIssuer           string
	JWTAudiences        map[string]struct{}
	JWTClockSkew        time.Duration
	JWKSRefreshInterval time.Duration
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

type requestRateLimiter interface {
	Allow(context.Context, string) (bool, error)
}

type localRateLimiter struct {
	inner *ipRateLimiter
}

type ipRateLimiter struct {
	limit  rate.Limit
	burst  int
	ttl    time.Duration
	mu     sync.Mutex
	bucket map[string]*ipBucket
}

type ipBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type redisRateLimiter struct {
	addr       string
	password   string
	db         int
	tlsEnabled bool
	keyPrefix  string
	window     time.Duration
	limit      int64
}

type authenticator struct {
	apiKeys  []string
	verifier *jwtVerifier
}

type jwtVerifier struct {
	jwksURL         string
	issuer          string
	audiences       map[string]struct{}
	leeway          time.Duration
	refreshInterval time.Duration
	client          *http.Client

	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
	fallback string
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type watermarkQuery struct {
	Cluster string
	Status  string
	Limit   int
}

type replayJobsQuery struct {
	Cluster string
	Status  string
	Limit   int
}

func main() {
	loadDotEnv()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	pgDSN := strings.TrimSpace(os.Getenv("HELIOS_PG_DSN"))
	if pgDSN == "" {
		log.Fatal("HELIOS_PG_DSN is required (use a Neon connection string, typically with sslmode=require)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("pg connect: %v", err)
	}
	defer pool.Close()

	s := &Server{db: pool}
	r, err := newRouter(cfg, s)
	if err != nil {
		log.Fatalf("router: %v", err)
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	log.Printf(
		"api listening on %s (auth=%t, jwks=%t, rate_limit=%s %.2f rps burst=%d)",
		cfg.Addr,
		len(cfg.APIKeys) > 0,
		cfg.JWTJWKSURL != "",
		cfg.RateLimitStore,
		cfg.RateLimitRPS,
		cfg.RateLimitBurst,
	)
	log.Fatal(server.ListenAndServe())
}

func newRouter(cfg APIConfig, s *Server) (http.Handler, error) {
	auth, err := newAuthenticator(cfg)
	if err != nil {
		return nil, err
	}
	limiter, err := newRateLimiter(cfg)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(cfg.RequestTimeout))
	r.Use(securityHeadersMiddleware)
	r.Use(corsMiddleware(cfg.CORSAllowOrigins))
	r.Use(maxBodyBytesMiddleware(cfg.MaxBodyBytes))

	r.Get("/", handleDashboard)
	r.Get("/healthz", handleHealth)
	r.Get("/readyz", s.handleReady(cfg.ReadyTimeout))

	r.Group(func(pr chi.Router) {
		if auth != nil {
			pr.Use(auth.middleware)
		}
		pr.Use(rateLimitMiddleware(limiter, cfg.RateLimitFailOpen))
		pr.Handle("/metrics", promhttp.Handler())
		pr.With(openAPITokenMiddleware(cfg.OpenAPIToken)).Get("/openapi.yaml", handleOpenAPISpec)
		pr.Get("/v1/watermarks", s.handleWatermarks)
		pr.Get("/v1/replay/jobs", s.handleReplayJobs)
	})

	return r, nil
}

func loadConfig() (APIConfig, error) {
	var cfg APIConfig
	var err error

	cfg.Addr = getenv("HELIOS_HTTP_ADDR", ":8090")

	cfg.ReadTimeout, err = getenvDuration("HELIOS_HTTP_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.ReadHeaderTimeout, err = getenvDuration("HELIOS_HTTP_READ_HEADER_TIMEOUT", 5*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.WriteTimeout, err = getenvDuration("HELIOS_HTTP_WRITE_TIMEOUT", 20*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.IdleTimeout, err = getenvDuration("HELIOS_HTTP_IDLE_TIMEOUT", 120*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.RequestTimeout, err = getenvDuration("HELIOS_HTTP_REQUEST_TIMEOUT", 15*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.ReadyTimeout, err = getenvDuration("HELIOS_READY_TIMEOUT", 2*time.Second)
	if err != nil {
		return cfg, err
	}

	cfg.MaxBodyBytes, err = getenvInt64("HELIOS_HTTP_MAX_BODY_BYTES", 1_048_576)
	if err != nil {
		return cfg, err
	}

	cfg.RateLimitRPS, err = getenvFloat64("HELIOS_RATE_LIMIT_RPS", 30)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitBurst, err = getenvInt("HELIOS_RATE_LIMIT_BURST", 60)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitStore = strings.ToLower(getenv("HELIOS_RATE_LIMIT_STORE", "memory"))
	if cfg.RateLimitStore != "memory" && cfg.RateLimitStore != "redis" {
		return cfg, errors.New("HELIOS_RATE_LIMIT_STORE must be memory or redis")
	}
	cfg.RateLimitWindow, err = getenvDuration("HELIOS_RATE_LIMIT_WINDOW", time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitFailOpen, err = getenvBool("HELIOS_RATE_LIMIT_FAIL_OPEN", false)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitRedisAddr = strings.TrimSpace(os.Getenv("HELIOS_RATE_LIMIT_REDIS_ADDR"))
	cfg.RateLimitRedisDB, err = getenvIntAllowZero("HELIOS_RATE_LIMIT_REDIS_DB", 0)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitRedisPass = strings.TrimSpace(os.Getenv("HELIOS_RATE_LIMIT_REDIS_PASSWORD"))
	cfg.RateLimitRedisTLS, err = getenvBool("HELIOS_RATE_LIMIT_REDIS_TLS", false)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitRedisKey = getenv("HELIOS_RATE_LIMIT_REDIS_PREFIX", "helios:rl")
	if cfg.RateLimitStore == "redis" && cfg.RateLimitRedisAddr == "" {
		return cfg, errors.New("HELIOS_RATE_LIMIT_REDIS_ADDR is required when HELIOS_RATE_LIMIT_STORE=redis")
	}

	cfg.APIKeys = splitCSV(os.Getenv("HELIOS_API_KEYS"))
	cfg.CORSAllowOrigins = toSet(splitCSV(os.Getenv("HELIOS_CORS_ALLOW_ORIGINS")))
	cfg.OpenAPIToken = strings.TrimSpace(os.Getenv("HELIOS_OPENAPI_TOKEN"))
	cfg.JWTJWKSURL = strings.TrimSpace(os.Getenv("HELIOS_JWT_JWKS_URL"))
	cfg.JWTIssuer = strings.TrimSpace(os.Getenv("HELIOS_JWT_ISSUER"))
	cfg.JWTAudiences = toSet(splitCSV(os.Getenv("HELIOS_JWT_AUDIENCE")))
	cfg.JWTClockSkew, err = getenvDuration("HELIOS_JWT_CLOCK_SKEW", 30*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.JWKSRefreshInterval, err = getenvDuration("HELIOS_JWKS_REFRESH_INTERVAL", 5*time.Minute)
	if err != nil {
		return cfg, err
	}
	if cfg.JWTJWKSURL != "" {
		u, err := url.Parse(cfg.JWTJWKSURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(u.Host) == "" {
			return cfg, errors.New("HELIOS_JWT_JWKS_URL must be a valid http/https URL")
		}
	}
	return cfg, nil
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := apiAssetsFS.ReadFile("web/index.html")
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "dashboard_unavailable", "dashboard unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.db == nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := s.db.Ping(ctx); err != nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

func handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	body, err := apiAssetsFS.ReadFile("openapi.yaml")
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "openapi_unavailable", "openapi unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) handleWatermarks(w http.ResponseWriter, r *http.Request) {
	q, err := parseWatermarkQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	if s == nil || s.db == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	query := "SELECT cluster, status, slot, updated_at FROM finality_watermarks"
	args := make([]any, 0, 3)
	filters := make([]string, 0, 2)
	if q.Cluster != "" {
		args = append(args, q.Cluster)
		filters = append(filters, fmt.Sprintf("cluster = $%d", len(args)))
	}
	if q.Status != "" {
		args = append(args, q.Status)
		filters = append(filters, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(filters) > 0 {
		query += " WHERE " + strings.Join(filters, " AND ")
	}
	args = append(args, q.Limit)
	query += fmt.Sprintf(" ORDER BY cluster, status LIMIT $%d", len(args))

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "query_failed", "query failed")
		return
	}
	defer rows.Close()

	type Watermark struct {
		Cluster   string    `json:"cluster"`
		Status    string    `json:"status"`
		Slot      int64     `json:"slot"`
		UpdatedAt time.Time `json:"updated_at"`
	}

	out := make([]Watermark, 0, q.Limit)
	for rows.Next() {
		var wm Watermark
		if err := rows.Scan(&wm.Cluster, &wm.Status, &wm.Slot, &wm.UpdatedAt); err != nil {
			writeAPIError(w, r, http.StatusInternalServerError, "scan_failed", "scan failed")
			return
		}
		out = append(out, wm)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReplayJobs(w http.ResponseWriter, r *http.Request) {
	q, err := parseReplayJobsQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	if s == nil || s.db == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	query := "SELECT job_id, cluster, slot_start, slot_end, status, created_at, updated_at, COALESCE(error,'') FROM replay_jobs"
	args := make([]any, 0, 3)
	filters := make([]string, 0, 2)
	if q.Cluster != "" {
		args = append(args, q.Cluster)
		filters = append(filters, fmt.Sprintf("cluster = $%d", len(args)))
	}
	if q.Status != "" {
		args = append(args, q.Status)
		filters = append(filters, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(filters) > 0 {
		query += " WHERE " + strings.Join(filters, " AND ")
	}
	args = append(args, q.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "query_failed", "query failed")
		return
	}
	defer rows.Close()

	type ReplayJob struct {
		JobID     string    `json:"job_id"`
		Cluster   string    `json:"cluster"`
		SlotStart int64     `json:"slot_start"`
		SlotEnd   int64     `json:"slot_end"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Error     string    `json:"error"`
	}

	out := make([]ReplayJob, 0, q.Limit)
	for rows.Next() {
		var j ReplayJob
		if err := rows.Scan(&j.JobID, &j.Cluster, &j.SlotStart, &j.SlotEnd, &j.Status, &j.CreatedAt, &j.UpdatedAt, &j.Error); err != nil {
			writeAPIError(w, r, http.StatusInternalServerError, "scan_failed", "scan failed")
			return
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, out)
}

func parseWatermarkQuery(values url.Values) (watermarkQuery, error) {
	if err := rejectUnknownQuery(values, map[string]struct{}{"cluster": {}, "status": {}, "limit": {}}); err != nil {
		return watermarkQuery{}, err
	}
	q := watermarkQuery{
		Cluster: strings.TrimSpace(values.Get("cluster")),
		Status:  strings.TrimSpace(values.Get("status")),
		Limit:   500,
	}
	if q.Cluster != "" && !isSafeToken(q.Cluster) {
		return q, errors.New("cluster must be alphanumeric with - or _")
	}
	if q.Status != "" && !isAllowed(q.Status, map[string]struct{}{"processed": {}, "confirmed": {}, "finalized": {}}) {
		return q, errors.New("status must be one of processed, confirmed, finalized")
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > 5000 {
			return q, errors.New("limit must be between 1 and 5000")
		}
		q.Limit = limit
	}
	return q, nil
}

func parseReplayJobsQuery(values url.Values) (replayJobsQuery, error) {
	if err := rejectUnknownQuery(values, map[string]struct{}{"cluster": {}, "status": {}, "limit": {}}); err != nil {
		return replayJobsQuery{}, err
	}
	q := replayJobsQuery{
		Cluster: strings.TrimSpace(values.Get("cluster")),
		Status:  strings.TrimSpace(values.Get("status")),
		Limit:   50,
	}
	if q.Cluster != "" && !isSafeToken(q.Cluster) {
		return q, errors.New("cluster must be alphanumeric with - or _")
	}
	if q.Status != "" && !isAllowed(q.Status, map[string]struct{}{"queued": {}, "running": {}, "failed": {}, "done": {}}) {
		return q, errors.New("status must be one of queued, running, failed, done")
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > 500 {
			return q, errors.New("limit must be between 1 and 500")
		}
		q.Limit = limit
	}
	return q, nil
}

func rejectUnknownQuery(values url.Values, allowed map[string]struct{}) error {
	for k := range values {
		if _, ok := allowed[k]; !ok {
			return fmt.Errorf("unsupported query parameter: %s", k)
		}
	}
	return nil
}

func isAllowed(value string, allowed map[string]struct{}) bool {
	_, ok := allowed[value]
	return ok
}

func isSafeToken(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	resp := apiErrorEnvelope{Error: apiError{Code: code, Message: message}}
	if r != nil {
		resp.Error.RequestID = middleware.GetReqID(r.Context())
	}
	writeJSON(w, status, resp)
}

func newAuthenticator(cfg APIConfig) (*authenticator, error) {
	var verifier *jwtVerifier
	if cfg.JWTJWKSURL != "" {
		v, err := newJWTVerifier(cfg)
		if err != nil {
			return nil, err
		}
		verifier = v
	}
	if len(cfg.APIKeys) == 0 && verifier == nil {
		return nil, nil
	}
	return &authenticator{apiKeys: cfg.APIKeys, verifier: verifier}, nil
}

func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeAPIError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
	})
}

func (a *authenticator) authorized(r *http.Request) bool {
	if a == nil {
		return true
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" && isAuthorizedAPIKey(key, a.apiKeys) {
		return true
	}
	bearer := extractBearerToken(r)
	if bearer == "" {
		return false
	}
	if a.verifier != nil {
		return a.verifier.Verify(r.Context(), bearer) == nil
	}
	return isAuthorizedAPIKey(bearer, a.apiKeys)
}

func extractBearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func isAuthorizedAPIKey(provided string, keys []string) bool {
	p := []byte(strings.TrimSpace(provided))
	if len(p) == 0 {
		return false
	}
	for _, key := range keys {
		k := []byte(strings.TrimSpace(key))
		if len(k) == len(p) && subtle.ConstantTimeCompare(k, p) == 1 {
			return true
		}
	}
	return false
}

func openAPITokenMiddleware(token string) func(http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimSpace(r.Header.Get("X-OpenAPI-Token"))
			p := []byte(provided)
			e := []byte(token)
			if len(p) == 0 || len(p) != len(e) || subtle.ConstantTimeCompare(p, e) != 1 {
				writeAPIError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func newJWTVerifier(cfg APIConfig) (*jwtVerifier, error) {
	return &jwtVerifier{
		jwksURL:         cfg.JWTJWKSURL,
		issuer:          cfg.JWTIssuer,
		audiences:       cfg.JWTAudiences,
		leeway:          cfg.JWTClockSkew,
		refreshInterval: cfg.JWKSRefreshInterval,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
		keys: make(map[string]*rsa.PublicKey),
	}, nil
}

func (v *jwtVerifier) Verify(ctx context.Context, token string) error {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return errors.New("invalid token")
	}

	headerPayload := parts[0] + "." + parts[1]
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("invalid token header")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("invalid token claims")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("invalid token signature")
	}

	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return errors.New("invalid token header")
	}
	if !strings.EqualFold(header.Alg, "RS256") {
		return errors.New("unsupported token algorithm")
	}

	pub, err := v.signingKey(ctx, strings.TrimSpace(header.Kid))
	if err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(headerPayload))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], signature); err != nil {
		return errors.New("invalid token signature")
	}

	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return errors.New("invalid token claims")
	}
	if err := validateJWTClaims(claims, v.issuer, v.audiences, time.Now().UTC(), v.leeway); err != nil {
		return err
	}
	return nil
}

func (v *jwtVerifier) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok := v.currentKey(kid); ok {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	if key, ok := v.currentKey(kid); ok {
		return key, nil
	}
	return nil, errors.New("signing key not found")
}

func (v *jwtVerifier) currentKey(kid string) (*rsa.PublicKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.keys) == 0 {
		return nil, false
	}
	if v.fetched.IsZero() || time.Since(v.fetched) > v.refreshInterval {
		return nil, false
	}
	if kid != "" {
		key, ok := v.keys[kid]
		return key, ok
	}
	if v.fallback != "" {
		key, ok := v.keys[v.fallback]
		return key, ok
	}
	if len(v.keys) == 1 {
		for _, key := range v.keys {
			return key, true
		}
	}
	return nil, false
}

func (v *jwtVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch failed: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}

	type jwk struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
		Alg string `json:"alg"`
		Use string `json:"use"`
	}
	type jwksDocument struct {
		Keys []jwk `json:"keys"`
	}

	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	fallback := ""
	for idx, k := range doc.Keys {
		if !strings.EqualFold(strings.TrimSpace(k.Kty), "RSA") {
			continue
		}
		if alg := strings.TrimSpace(k.Alg); alg != "" && !strings.EqualFold(alg, "RS256") {
			continue
		}
		if use := strings.TrimSpace(k.Use); use != "" && !strings.EqualFold(use, "sig") {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		kid := strings.TrimSpace(k.Kid)
		if kid == "" {
			kid = fmt.Sprintf("fallback-%d", idx)
		}
		keys[kid] = pub
		if fallback == "" {
			fallback = kid
		}
	}
	if len(keys) == 0 {
		return errors.New("jwks has no usable RSA keys")
	}

	v.mu.Lock()
	v.keys = keys
	v.fallback = fallback
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}

func parseRSAPublicKey(nEncoded, eEncoded string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(nEncoded))
	if err != nil || len(nBytes) == 0 {
		return nil, errors.New("invalid jwk n")
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(eEncoded))
	if err != nil || len(eBytes) == 0 {
		return nil, errors.New("invalid jwk e")
	}

	e := 0
	for _, b := range eBytes {
		e = (e << 8) | int(b)
	}
	if e <= 1 {
		return nil, errors.New("invalid jwk exponent")
	}

	modulus := new(big.Int).SetBytes(nBytes)
	if modulus.Sign() <= 0 {
		return nil, errors.New("invalid jwk modulus")
	}
	return &rsa.PublicKey{N: modulus, E: e}, nil
}

func validateJWTClaims(claims map[string]any, issuer string, audiences map[string]struct{}, now time.Time, leeway time.Duration) error {
	exp, ok := claimUnixSeconds(claims["exp"])
	if !ok {
		return errors.New("token exp missing")
	}
	if now.After(time.Unix(exp, 0).Add(leeway)) {
		return errors.New("token expired")
	}
	if nbf, ok := claimUnixSeconds(claims["nbf"]); ok {
		if now.Before(time.Unix(nbf, 0).Add(-leeway)) {
			return errors.New("token not active")
		}
	}
	if iat, ok := claimUnixSeconds(claims["iat"]); ok {
		if now.Before(time.Unix(iat, 0).Add(-leeway)) {
			return errors.New("token issued in the future")
		}
	}
	if issuer != "" {
		iss, ok := claims["iss"].(string)
		if !ok || strings.TrimSpace(iss) != issuer {
			return errors.New("token issuer mismatch")
		}
	}
	if len(audiences) > 0 {
		aud, ok := parseAudienceClaim(claims["aud"])
		if !ok {
			return errors.New("token audience missing")
		}
		matched := false
		for _, a := range aud {
			if _, exists := audiences[a]; exists {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("token audience mismatch")
		}
	}
	return nil
}

func claimUnixSeconds(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func parseAudienceClaim(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, false
		}
		return []string{v}, true
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

func newRateLimiter(cfg APIConfig) (requestRateLimiter, error) {
	if cfg.RateLimitStore == "redis" {
		return &redisRateLimiter{
			addr:       cfg.RateLimitRedisAddr,
			password:   cfg.RateLimitRedisPass,
			db:         cfg.RateLimitRedisDB,
			tlsEnabled: cfg.RateLimitRedisTLS,
			keyPrefix:  cfg.RateLimitRedisKey,
			window:     cfg.RateLimitWindow,
			limit:      int64(cfg.RateLimitBurst),
		}, nil
	}
	return &localRateLimiter{
		inner: newIPRateLimiter(rate.Limit(cfg.RateLimitRPS), cfg.RateLimitBurst, 10*time.Minute),
	}, nil
}

func (l *localRateLimiter) Allow(_ context.Context, key string) (bool, error) {
	return l.inner.get(key).Allow(), nil
}

func (l *redisRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	if strings.TrimSpace(key) == "" {
		key = "unknown"
	}
	windowSeconds := int64(math.Ceil(l.window.Seconds()))
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	bucket := time.Now().Unix() / windowSeconds
	redisKey := fmt.Sprintf("%s:%s:%d", l.keyPrefix, key, bucket)

	conn, reader, err := l.dial(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	if l.password != "" {
		if err := redisSimpleCommand(conn, reader, "AUTH", l.password); err != nil {
			return false, err
		}
	}
	if l.db > 0 {
		if err := redisSimpleCommand(conn, reader, "SELECT", strconv.Itoa(l.db)); err != nil {
			return false, err
		}
	}
	count, err := redisIntCommand(conn, reader, "INCR", redisKey)
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err := redisSimpleCommand(conn, reader, "EXPIRE", redisKey, strconv.FormatInt(windowSeconds+1, 10)); err != nil {
			return false, err
		}
	}
	if count > l.limit {
		return false, nil
	}
	return true, nil
}

func (l *redisRateLimiter) dial(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", l.addr)
	if err != nil {
		return nil, nil, err
	}
	if !l.tlsEnabled {
		_ = rawConn.SetDeadline(time.Now().Add(2 * time.Second))
		return rawConn, bufio.NewReader(rawConn), nil
	}
	host, _, err := net.SplitHostPort(l.addr)
	if err != nil {
		host = l.addr
	}
	tlsConn := tls.Client(rawConn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, nil, err
	}
	_ = tlsConn.SetDeadline(time.Now().Add(2 * time.Second))
	return tlsConn, bufio.NewReader(tlsConn), nil
}

func redisSimpleCommand(conn net.Conn, reader *bufio.Reader, args ...string) error {
	if err := writeRESPCommand(conn, args...); err != nil {
		return err
	}
	typeByte, payload, err := readRESPLine(reader)
	if err != nil {
		return err
	}
	switch typeByte {
	case '+':
		return nil
	case ':':
		if payload == "1" || payload == "0" {
			return nil
		}
		return nil
	case '-':
		return errors.New(payload)
	default:
		return fmt.Errorf("unexpected redis response type %q", string(typeByte))
	}
}

func redisIntCommand(conn net.Conn, reader *bufio.Reader, args ...string) (int64, error) {
	if err := writeRESPCommand(conn, args...); err != nil {
		return 0, err
	}
	typeByte, payload, err := readRESPLine(reader)
	if err != nil {
		return 0, err
	}
	switch typeByte {
	case ':':
		return strconv.ParseInt(payload, 10, 64)
	case '-':
		return 0, errors.New(payload)
	default:
		return 0, fmt.Errorf("unexpected redis response type %q", string(typeByte))
	}
}

func writeRESPCommand(conn net.Conn, args ...string) error {
	if _, err := fmt.Fprintf(conn, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func readRESPLine(reader *bufio.Reader) (byte, string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return 0, "", errors.New("empty redis response")
	}
	return line[0], line[1:], nil
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(allowOrigins map[string]struct{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" {
				_, allowed := allowOrigins[origin]
				if allowed {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-OpenAPI-Token")
					w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					w.Header().Set("Access-Control-Max-Age", "600")
					w.Header().Set("Vary", "Origin")
				}
				if r.Method == http.MethodOptions {
					if !allowed {
						writeAPIError(w, r, http.StatusForbidden, "origin_not_allowed", "origin not allowed")
						return
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func maxBodyBytesMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func newIPRateLimiter(limit rate.Limit, burst int, ttl time.Duration) *ipRateLimiter {
	l := &ipRateLimiter{
		limit:  limit,
		burst:  burst,
		ttl:    ttl,
		bucket: make(map[string]*ipBucket),
	}
	go l.cleanupLoop()
	return l
}

func (l *ipRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-l.ttl)
		l.mu.Lock()
		for ip, bucket := range l.bucket {
			if bucket.lastSeen.Before(cutoff) {
				delete(l.bucket, ip)
			}
		}
		l.mu.Unlock()
	}
}

func (l *ipRateLimiter) get(ip string) *rate.Limiter {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if bucket, ok := l.bucket[ip]; ok {
		bucket.lastSeen = now
		return bucket.limiter
	}
	limiter := rate.NewLimiter(l.limit, l.burst)
	l.bucket[ip] = &ipBucket{
		limiter:  limiter,
		lastSeen: now,
	}
	return limiter
}

func rateLimitMiddleware(limiter requestRateLimiter, failOpen bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}
			allowed, err := limiter.Allow(r.Context(), clientIP(r))
			if err != nil {
				if failOpen {
					next.ServeHTTP(w, r)
					return
				}
				writeAPIError(w, r, http.StatusServiceUnavailable, "rate_limiter_unavailable", "rate limiter unavailable")
				return
			}
			if !allowed {
				w.Header().Set("Retry-After", "1")
				writeAPIError(w, r, http.StatusTooManyRequests, "rate_limit_exceeded", "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if strings.TrimSpace(r.RemoteAddr) != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "unknown"
}

func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		v := strings.TrimSpace(item)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func toSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		return 0, errors.New(key + ": invalid duration")
	}
	return v, nil
}

func getenvFloat64(key string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, errors.New(key + ": invalid float")
	}
	if v <= 0 {
		return 0, errors.New(key + ": must be > 0")
	}
	return v, nil
}

func getenvInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New(key + ": invalid int")
	}
	if v <= 0 {
		return 0, errors.New(key + ": must be > 0")
	}
	return v, nil
}

func getenvIntAllowZero(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New(key + ": invalid int")
	}
	if v < 0 {
		return 0, errors.New(key + ": must be >= 0")
	}
	return v, nil
}

func getenvInt64(key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New(key + ": invalid int64")
	}
	if v <= 0 {
		return 0, errors.New(key + ": must be > 0")
	}
	return v, nil
}

func getenvBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New(key + ": invalid bool")
	}
	return v, nil
}

func loadDotEnv() {
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../../.env")
}
