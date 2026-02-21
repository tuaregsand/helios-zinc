package main

import (
	"bufio"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig() APIConfig {
	return APIConfig{
		Addr:                ":0",
		ReadTimeout:         5 * time.Second,
		ReadHeaderTimeout:   2 * time.Second,
		WriteTimeout:        5 * time.Second,
		IdleTimeout:         30 * time.Second,
		RequestTimeout:      3 * time.Second,
		ReadyTimeout:        2 * time.Second,
		MaxBodyBytes:        1024 * 1024,
		RateLimitRPS:        100,
		RateLimitBurst:      100,
		RateLimitStore:      "memory",
		RateLimitWindow:     time.Second,
		RateLimitFailOpen:   false,
		RateLimitRedisKey:   "helios:rl",
		APIKeys:             []string{"test-key"},
		CORSAllowOrigins:    toSet([]string{"https://app.example.com"}),
		JWTClockSkew:        30 * time.Second,
		JWKSRefreshInterval: 5 * time.Minute,
	}
}

func newTestRouter(t *testing.T, cfg APIConfig) http.Handler {
	t.Helper()
	router, err := newRouter(cfg, &Server{})
	if err != nil {
		t.Fatalf("newRouter failed: %v", err)
	}
	return router
}

func parseErrorCode(t *testing.T, body string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("failed to parse error JSON: %v body=%q", err, body)
	}
	errorRaw, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error field missing in payload: %v", payload)
	}
	code, _ := errorRaw["code"].(string)
	return code
}

func TestProtectedRouteRequiresAuth(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if parseErrorCode(t, rec.Body.String()) != "unauthorized" {
		t.Fatalf("unexpected error payload: %s", rec.Body.String())
	}
}

func TestProtectedRouteAllowsValidAPIKey(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-API-Key", "test-key")
	req.RemoteAddr = "203.0.113.11:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProtectedRouteAllowsBearerWhenJWTDisabled(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.RemoteAddr = "203.0.113.12:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProtectedRouteAllowsValidJWT(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey failed: %v", err)
	}
	jwksServer := newJWKSserver(t, privateKey.PublicKey, "kid-1")
	defer jwksServer.Close()

	cfg := testConfig()
	cfg.APIKeys = nil
	cfg.JWTJWKSURL = jwksServer.URL
	cfg.JWTIssuer = "https://issuer.example"
	cfg.JWTAudiences = toSet([]string{"helios-api"})
	router := newTestRouter(t, cfg)

	now := time.Now().UTC()
	token := makeRS256Token(t, privateKey, "kid-1", map[string]any{
		"iss": "https://issuer.example",
		"aud": "helios-api",
		"exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(),
		"nbf": now.Add(-10 * time.Second).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "203.0.113.13:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProtectedRouteRejectsJWTWithWrongAudience(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey failed: %v", err)
	}
	jwksServer := newJWKSserver(t, privateKey.PublicKey, "kid-1")
	defer jwksServer.Close()

	cfg := testConfig()
	cfg.APIKeys = nil
	cfg.JWTJWKSURL = jwksServer.URL
	cfg.JWTIssuer = "https://issuer.example"
	cfg.JWTAudiences = toSet([]string{"helios-api"})
	router := newTestRouter(t, cfg)

	now := time.Now().UTC()
	token := makeRS256Token(t, privateKey, "kid-1", map[string]any{
		"iss": "https://issuer.example",
		"aud": "other-aud",
		"exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "203.0.113.14:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestOpenAPISpecProtectedByAuthAndToken(t *testing.T) {
	cfg := testConfig()
	cfg.OpenAPIToken = "docs-secret"
	router := newTestRouter(t, cfg)

	req1 := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req1.Header.Set("X-API-Key", "test-key")
	req1.RemoteAddr = "203.0.113.15:12345"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without docs token, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req2.Header.Set("X-API-Key", "test-key")
	req2.Header.Set("X-OpenAPI-Token", "docs-secret")
	req2.RemoteAddr = "203.0.113.16:12345"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with docs token, got %d", rec2.Code)
	}
	if body := rec2.Body.String(); !strings.HasPrefix(body, "openapi") {
		t.Fatalf("expected openapi body, got %q", body)
	}
}

func TestOpenAPISpecProtectedByAuth(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	req1 := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req1.RemoteAddr = "203.0.113.17:12345"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("without key expected 401, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req2.Header.Set("X-API-Key", "test-key")
	req2.RemoteAddr = "203.0.113.18:12345"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("with key expected 200, got %d", rec2.Code)
	}
}

func TestRateLimitBlocksBurstMemory(t *testing.T) {
	cfg := testConfig()
	cfg.APIKeys = nil
	cfg.RateLimitRPS = 1
	cfg.RateLimitBurst = 1
	router := newTestRouter(t, cfg)

	req1 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req1.RemoteAddr = "203.0.113.20:12345"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request expected 200, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req2.RemoteAddr = "203.0.113.20:12345"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request expected 429, got %d", rec2.Code)
	}
	if parseErrorCode(t, rec2.Body.String()) != "rate_limit_exceeded" {
		t.Fatalf("unexpected 429 payload: %s", rec2.Body.String())
	}
}

func TestRateLimitRedisBlocksBurst(t *testing.T) {
	redisAddr, shutdown := startFakeRedis(t)
	defer shutdown()

	cfg := testConfig()
	cfg.APIKeys = nil
	cfg.RateLimitStore = "redis"
	cfg.RateLimitRedisAddr = redisAddr
	cfg.RateLimitBurst = 1
	cfg.RateLimitWindow = time.Second
	router := newTestRouter(t, cfg)

	req1 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req1.RemoteAddr = "203.0.113.30:12345"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request expected 200, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req2.RemoteAddr = "203.0.113.30:12345"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request expected 429, got %d", rec2.Code)
	}
}

func TestRateLimitFailOpenWhenRedisUnavailable(t *testing.T) {
	cfg := testConfig()
	cfg.APIKeys = nil
	cfg.RateLimitStore = "redis"
	cfg.RateLimitRedisAddr = "127.0.0.1:1"
	cfg.RateLimitFailOpen = true
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.31:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with fail-open, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRateLimitFailsClosedWhenRedisUnavailable(t *testing.T) {
	cfg := testConfig()
	cfg.APIKeys = nil
	cfg.RateLimitStore = "redis"
	cfg.RateLimitRedisAddr = "127.0.0.1:1"
	cfg.RateLimitFailOpen = false
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.32:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with fail-closed, got %d", rec.Code)
	}
	if parseErrorCode(t, rec.Body.String()) != "rate_limiter_unavailable" {
		t.Fatalf("unexpected payload: %s", rec.Body.String())
	}
}

func TestHealthAndReadyEndpoints(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthReq.RemoteAddr = "203.0.113.40:12345"
	healthRec := httptest.NewRecorder()
	router.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health expected 200, got %d", healthRec.Code)
	}
	if !strings.Contains(healthRec.Body.String(), "ok") {
		t.Fatalf("unexpected health body: %s", healthRec.Body.String())
	}

	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyReq.RemoteAddr = "203.0.113.41:12345"
	readyRec := httptest.NewRecorder()
	router.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready expected 503 without db, got %d", readyRec.Code)
	}
	if parseErrorCode(t, readyRec.Body.String()) != "database_unavailable" {
		t.Fatalf("unexpected ready payload: %s", readyRec.Body.String())
	}
}

func TestCORSMiddlewareAllowedOriginOptions(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodOptions, "/v1/watermarks", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.RemoteAddr = "203.0.113.50:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected allow-origin header, got %q", got)
	}
}

func TestCORSMiddlewareDisallowedOriginOptions(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	req := httptest.NewRequest(http.MethodOptions, "/v1/watermarks", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.RemoteAddr = "203.0.113.51:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if parseErrorCode(t, rec.Body.String()) != "origin_not_allowed" {
		t.Fatalf("unexpected CORS payload: %s", rec.Body.String())
	}
}

func TestWatermarksQueryValidation(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	cases := []string{
		"/v1/watermarks?status=bad",
		"/v1/watermarks?limit=0",
		"/v1/watermarks?bad=1",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", "test-key")
		req.RemoteAddr = "203.0.113.60:12345"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("path %s expected 400 got %d", path, rec.Code)
		}
		if parseErrorCode(t, rec.Body.String()) != "invalid_query" {
			t.Fatalf("path %s expected invalid_query, body=%s", path, rec.Body.String())
		}
	}
}

func TestReplayJobsQueryValidation(t *testing.T) {
	cfg := testConfig()
	router := newTestRouter(t, cfg)

	cases := []string{
		"/v1/replay/jobs?status=weird",
		"/v1/replay/jobs?limit=1000",
		"/v1/replay/jobs?noop=1",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", "test-key")
		req.RemoteAddr = "203.0.113.61:12345"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("path %s expected 400 got %d", path, rec.Code)
		}
		if parseErrorCode(t, rec.Body.String()) != "invalid_query" {
			t.Fatalf("path %s expected invalid_query, body=%s", path, rec.Body.String())
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":8090" {
		t.Fatalf("expected default addr, got %q", cfg.Addr)
	}
	if cfg.ReadyTimeout != 2*time.Second {
		t.Fatalf("unexpected ready timeout: %v", cfg.ReadyTimeout)
	}
	if cfg.RateLimitStore != "memory" {
		t.Fatalf("unexpected rate limit store: %s", cfg.RateLimitStore)
	}
	if cfg.RateLimitWindow != time.Second {
		t.Fatalf("unexpected rate limit window: %v", cfg.RateLimitWindow)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HELIOS_RATE_LIMIT_STORE", "invalid")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for invalid rate limit store")
	}

	clearConfigEnv(t)
	t.Setenv("HELIOS_RATE_LIMIT_STORE", "redis")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for missing redis addr")
	}

	clearConfigEnv(t)
	t.Setenv("HELIOS_JWT_JWKS_URL", "not-a-url")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for invalid jwks url")
	}
}

func TestParseWatermarkQuery(t *testing.T) {
	q, err := parseWatermarkQuery(map[string][]string{
		"cluster": {"mainnet-beta"},
		"status":  {"finalized"},
		"limit":   {"100"},
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if q.Cluster != "mainnet-beta" || q.Status != "finalized" || q.Limit != 100 {
		t.Fatalf("unexpected query: %+v", q)
	}
}

func TestParseReplayJobsQuery(t *testing.T) {
	q, err := parseReplayJobsQuery(map[string][]string{
		"cluster": {"mainnet-beta"},
		"status":  {"queued"},
		"limit":   {"20"},
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if q.Cluster != "mainnet-beta" || q.Status != "queued" || q.Limit != 20 {
		t.Fatalf("unexpected query: %+v", q)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"HELIOS_HTTP_ADDR",
		"HELIOS_HTTP_READ_TIMEOUT",
		"HELIOS_HTTP_READ_HEADER_TIMEOUT",
		"HELIOS_HTTP_WRITE_TIMEOUT",
		"HELIOS_HTTP_IDLE_TIMEOUT",
		"HELIOS_HTTP_REQUEST_TIMEOUT",
		"HELIOS_READY_TIMEOUT",
		"HELIOS_HTTP_MAX_BODY_BYTES",
		"HELIOS_RATE_LIMIT_RPS",
		"HELIOS_RATE_LIMIT_BURST",
		"HELIOS_RATE_LIMIT_STORE",
		"HELIOS_RATE_LIMIT_WINDOW",
		"HELIOS_RATE_LIMIT_FAIL_OPEN",
		"HELIOS_RATE_LIMIT_REDIS_ADDR",
		"HELIOS_RATE_LIMIT_REDIS_DB",
		"HELIOS_RATE_LIMIT_REDIS_PASSWORD",
		"HELIOS_RATE_LIMIT_REDIS_TLS",
		"HELIOS_RATE_LIMIT_REDIS_PREFIX",
		"HELIOS_API_KEYS",
		"HELIOS_CORS_ALLOW_ORIGINS",
		"HELIOS_OPENAPI_TOKEN",
		"HELIOS_JWT_JWKS_URL",
		"HELIOS_JWT_ISSUER",
		"HELIOS_JWT_AUDIENCE",
		"HELIOS_JWT_CLOCK_SKEW",
		"HELIOS_JWKS_REFRESH_INTERVAL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func newJWKSserver(t *testing.T, pub rsa.PublicKey, kid string) *httptest.Server {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	payload := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"%s","use":"sig","alg":"RS256","n":"%s","e":"%s"}]}`, kid, n, e)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
}

func makeRS256Token(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString(headerJSON)
	payload := enc.EncodeToString(claimsJSON)
	signingInput := header + "." + payload
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signingInput + "." + enc.EncodeToString(sig)
}

func startFakeRedis(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake redis: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	state := struct {
		mu    sync.Mutex
		count map[string]int64
	}{count: map[string]int64{}}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					return
				}
			}
			wg.Add(1)
			go func(conn net.Conn) {
				defer wg.Done()
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					args, err := readRESPArray(reader)
					if err != nil {
						if err == io.EOF {
							return
						}
						return
					}
					if len(args) == 0 {
						return
					}
					cmd := strings.ToUpper(args[0])
					switch cmd {
					case "AUTH", "SELECT":
						_, _ = conn.Write([]byte("+OK\r\n"))
					case "INCR":
						if len(args) != 2 {
							_, _ = conn.Write([]byte("-ERR wrong number of arguments\r\n"))
							continue
						}
						state.mu.Lock()
						state.count[args[1]]++
						val := state.count[args[1]]
						state.mu.Unlock()
						_, _ = conn.Write([]byte(":" + strconv.FormatInt(val, 10) + "\r\n"))
					case "EXPIRE":
						_, _ = conn.Write([]byte(":1\r\n"))
					default:
						_, _ = conn.Write([]byte("-ERR unknown command\r\n"))
					}
				}
			}(conn)
		}
	}()

	return ln.Addr().String(), func() {
		close(stop)
		_ = ln.Close()
		wg.Wait()
	}
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
	head, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	head = strings.TrimSuffix(strings.TrimSuffix(head, "\n"), "\r")
	if len(head) < 2 || head[0] != '*' {
		return nil, fmt.Errorf("invalid array header: %q", head)
	}
	n, err := strconv.Atoi(head[1:])
	if err != nil || n < 0 {
		return nil, fmt.Errorf("invalid array len: %q", head)
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		bulkHeader, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		bulkHeader = strings.TrimSuffix(strings.TrimSuffix(bulkHeader, "\n"), "\r")
		if len(bulkHeader) < 2 || bulkHeader[0] != '$' {
			return nil, fmt.Errorf("invalid bulk header: %q", bulkHeader)
		}
		sz, err := strconv.Atoi(bulkHeader[1:])
		if err != nil || sz < 0 {
			return nil, fmt.Errorf("invalid bulk size: %q", bulkHeader)
		}
		buf := make([]byte, sz+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:sz]))
	}
	return args, nil
}
