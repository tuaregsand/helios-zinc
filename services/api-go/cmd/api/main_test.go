package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig() APIConfig {
	return APIConfig{
		Addr:              ":0",
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		RequestTimeout:    3 * time.Second,
		MaxBodyBytes:      1024 * 1024,
		RateLimitRPS:      100,
		RateLimitBurst:    100,
		APIKeys:           []string{"test-key"},
		CORSAllowOrigins:  toSet([]string{"https://app.example.com"}),
	}
}

func TestProtectedRouteRequiresAPIKey(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestProtectedRouteAllowsValidAPIKey(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-API-Key", "test-key")
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProtectedRouteAllowsBearerToken(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.RemoteAddr = "203.0.113.11:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProtectedRouteWithoutKeyWhenAuthDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.APIKeys = nil
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.12:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRateLimitBlocksBurst(t *testing.T) {
	cfg := testConfig()
	cfg.RateLimitRPS = 1
	cfg.RateLimitBurst = 1
	router := newRouter(cfg, &Server{})

	req1 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req1.Header.Set("X-API-Key", "test-key")
	req1.RemoteAddr = "203.0.113.20:12345"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request expected 200, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req2.Header.Set("X-API-Key", "test-key")
	req2.RemoteAddr = "203.0.113.20:12345"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request expected 429, got %d", rec2.Code)
	}
}

func TestHealthIsPublicAndHasSecurityHeaders(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.13:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected body ok, got %q", rec.Body.String())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing frame options header")
	}
}

func TestCORSMiddlewareAllowedOriginOptions(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodOptions, "/v1/watermarks", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.RemoteAddr = "203.0.113.30:12345"
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
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodOptions, "/v1/watermarks", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.RemoteAddr = "203.0.113.31:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow-origin header, got %q", got)
	}
}

func TestOpenAPISpecRequiresKeyWhenConfigured(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req.RemoteAddr = "203.0.113.39:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestOpenAPISpecIsProtected(t *testing.T) {
	cfg := testConfig()
	router := newRouter(cfg, &Server{})

	req1 := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req1.RemoteAddr = "203.0.113.40:12345"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("without key expected 401, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	req2.Header.Set("X-API-Key", "test-key")
	req2.RemoteAddr = "203.0.113.41:12345"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("with key expected 200, got %d", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); ct == "" {
		t.Fatalf("expected content-type header")
	}
	if body := rec2.Body.String(); !strings.HasPrefix(body, "openapi") {
		t.Fatalf("expected openapi content, got %q", body)
	}
}

func TestExtractAPIKeyPreference(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "header-key")
	req.Header.Set("Authorization", "Bearer bearer-key")
	if got := extractAPIKey(req); got != "header-key" {
		t.Fatalf("expected header key, got %q", got)
	}
}

func TestSplitCSVTrimDedup(t *testing.T) {
	got := splitCSV(" a, b, a ,,c ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %d items got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %q at %d got %q", want[i], i, got[i])
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
	if cfg.ReadTimeout != 10*time.Second {
		t.Fatalf("unexpected read timeout: %v", cfg.ReadTimeout)
	}
	if cfg.RequestTimeout != 15*time.Second {
		t.Fatalf("unexpected request timeout: %v", cfg.RequestTimeout)
	}
	if cfg.MaxBodyBytes != 1048576 {
		t.Fatalf("unexpected max body bytes: %d", cfg.MaxBodyBytes)
	}
	if cfg.RateLimitRPS != 30 {
		t.Fatalf("unexpected rate limit rps: %f", cfg.RateLimitRPS)
	}
	if cfg.RateLimitBurst != 60 {
		t.Fatalf("unexpected rate limit burst: %d", cfg.RateLimitBurst)
	}
	if len(cfg.APIKeys) != 0 {
		t.Fatalf("expected no api keys, got %d", len(cfg.APIKeys))
	}
}

func TestLoadConfigInvalidDuration(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HELIOS_HTTP_READ_TIMEOUT", "bad")
	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestLoadConfigInvalidInt(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("HELIOS_RATE_LIMIT_BURST", "0")
	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error")
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
		"HELIOS_HTTP_MAX_BODY_BYTES",
		"HELIOS_RATE_LIMIT_RPS",
		"HELIOS_RATE_LIMIT_BURST",
		"HELIOS_API_KEYS",
		"HELIOS_CORS_ALLOW_ORIGINS",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}
