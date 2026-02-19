package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
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
	Addr              string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	MaxBodyBytes      int64
	RateLimitRPS      float64
	RateLimitBurst    int
	APIKeys           []string
	CORSAllowOrigins  map[string]struct{}
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
	r := newRouter(cfg, s)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	log.Printf(
		"api listening on %s (protected routes auth=%t, rate_limit=%.2f rps burst=%d)",
		cfg.Addr,
		len(cfg.APIKeys) > 0,
		cfg.RateLimitRPS,
		cfg.RateLimitBurst,
	)
	log.Fatal(server.ListenAndServe())
}

func newRouter(cfg APIConfig, s *Server) http.Handler {
	limiter := newIPRateLimiter(rate.Limit(cfg.RateLimitRPS), cfg.RateLimitBurst, 10*time.Minute)

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

	r.Group(func(pr chi.Router) {
		if len(cfg.APIKeys) > 0 {
			pr.Use(apiKeyMiddleware(cfg.APIKeys))
		}
		pr.Use(rateLimitMiddleware(limiter))
		pr.Handle("/metrics", promhttp.Handler())
		pr.Get("/openapi.yaml", handleOpenAPISpec)
		pr.Get("/v1/watermarks", s.handleWatermarks)
		pr.Get("/v1/replay/jobs", s.handleReplayJobs)
	})

	return r
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

	maxBodyBytes, err := getenvInt64("HELIOS_HTTP_MAX_BODY_BYTES", 1_048_576)
	if err != nil {
		return cfg, err
	}
	cfg.MaxBodyBytes = maxBodyBytes

	cfg.RateLimitRPS, err = getenvFloat64("HELIOS_RATE_LIMIT_RPS", 30)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimitBurst, err = getenvInt("HELIOS_RATE_LIMIT_BURST", 60)
	if err != nil {
		return cfg, err
	}
	cfg.APIKeys = splitCSV(os.Getenv("HELIOS_API_KEYS"))
	cfg.CORSAllowOrigins = toSet(splitCSV(os.Getenv("HELIOS_CORS_ALLOW_ORIGINS")))
	return cfg, nil
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := apiAssetsFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	body, err := apiAssetsFS.ReadFile("openapi.yaml")
	if err != nil {
		http.Error(w, "openapi unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) handleWatermarks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, "SELECT cluster, status, slot, updated_at FROM finality_watermarks ORDER BY cluster, status")
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Watermark struct {
		Cluster   string    `json:"cluster"`
		Status    string    `json:"status"`
		Slot      int64     `json:"slot"`
		UpdatedAt time.Time `json:"updated_at"`
	}

	out := make([]Watermark, 0, 16)
	for rows.Next() {
		var wm Watermark
		if err := rows.Scan(&wm.Cluster, &wm.Status, &wm.Slot, &wm.UpdatedAt); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		out = append(out, wm)
	}
	writeJSON(w, out)
}

func (s *Server) handleReplayJobs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, "SELECT job_id, cluster, slot_start, slot_end, status, created_at, updated_at, COALESCE(error,'') FROM replay_jobs ORDER BY created_at DESC LIMIT 50")
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
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

	out := make([]ReplayJob, 0, 50)
	for rows.Next() {
		var j ReplayJob
		if err := rows.Scan(&j.JobID, &j.Cluster, &j.SlotStart, &j.SlotEnd, &j.Status, &j.CreatedAt, &j.UpdatedAt, &j.Error); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		out = append(out, j)
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func apiKeyMiddleware(keys []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAuthorizedAPIKey(extractAPIKey(r), keys) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractAPIKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-API-Key")); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func isAuthorizedAPIKey(provided string, keys []string) bool {
	p := []byte(provided)
	if len(p) == 0 {
		return false
	}
	for _, key := range keys {
		k := []byte(key)
		if len(k) == len(p) && subtle.ConstantTimeCompare(k, p) == 1 {
			return true
		}
	}
	return false
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
				if _, ok := allowOrigins[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key")
					w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					w.Header().Set("Access-Control-Max-Age", "600")
					w.Header().Set("Vary", "Origin")
				}
				if r.Method == http.MethodOptions {
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

func rateLimitMiddleware(l *ipRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !l.get(ip).Allow() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
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
	if err != nil {
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

func loadDotEnv() {
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../../.env")
}
