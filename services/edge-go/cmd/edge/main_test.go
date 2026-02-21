package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return u
}

func testConfig(backends ...string) config {
	parsed := make([]*url.URL, 0, len(backends))
	for _, raw := range backends {
		u, _ := url.Parse(raw)
		parsed = append(parsed, u)
	}
	return config{
		Addr:                 ":0",
		BackendTargets:       parsed,
		BalancePolicy:        balanceRoundRobin,
		HealthPath:           "/healthz",
		HealthInterval:       time.Second,
		BackendTimeout:       time.Second,
		ReadTimeout:          5 * time.Second,
		ReadHeaderTimeout:    2 * time.Second,
		WriteTimeout:         5 * time.Second,
		IdleTimeout:          30 * time.Second,
		MaxBodyBytes:         1_048_576,
		CircuitFailThreshold: 2,
		CircuitOpenFor:       30 * time.Second,
		RetryAttempts:        2,
	}
}

func newEdgeWithTransport(t *testing.T, cfg config, transport roundTripFunc) *edgeServer {
	t.Helper()
	edge, err := newEdgeServer(cfg)
	if err != nil {
		t.Fatalf("newEdgeServer failed: %v", err)
	}
	client := &http.Client{Timeout: cfg.BackendTimeout, Transport: transport}
	edge.healthClient = client
	edge.upstreamClient = client
	return edge
}

func successResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func metricValue(t *testing.T, body, metric string, labels map[string]string) float64 {
	t.Helper()
	prefix := metric + "{"
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}
		end := strings.Index(line, "}")
		if end <= len(metric) {
			continue
		}
		lineLabels := parseMetricLabels(line[len(metric)+1 : end])
		if !labelsEqual(lineLabels, labels) {
			continue
		}
		valueRaw := strings.TrimSpace(line[end+1:])
		v, err := strconv.ParseFloat(valueRaw, 64)
		if err != nil {
			t.Fatalf("parse metric value %q: %v", valueRaw, err)
		}
		return v
	}
	t.Fatalf("metric %s with labels %v not found in body:\n%s", metric, labels, body)
	return 0
}

func parseMetricLabels(raw string) map[string]string {
	out := map[string]string{}
	for _, token := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(token), "=")
		if !ok {
			continue
		}
		out[k] = strings.Trim(v, "\"")
	}
	return out
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a, b, a, ,c ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("unexpected len: got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected item at %d: got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestParseBackendTargets(t *testing.T) {
	targets, err := parseBackendTargets("http://127.0.0.1:8090,https://api.example.com")
	if err != nil {
		t.Fatalf("parseBackendTargets failed: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("unexpected target count: %d", len(targets))
	}
	if targets[0].String() != "http://127.0.0.1:8090" {
		t.Fatalf("unexpected first target: %s", targets[0].String())
	}
}

func TestParseBackendTargetsRejectsInvalid(t *testing.T) {
	cases := []string{"", "127.0.0.1:8090", "http://", "http://api.example.com?q=1"}
	for _, tc := range cases {
		if _, err := parseBackendTargets(tc); err == nil {
			t.Fatalf("expected error for input %q", tc)
		}
	}
}

func TestBackendPoolRoundRobinAndLeastConn(t *testing.T) {
	a := &backend{target: mustURL(t, "http://a.test"), label: "a"}
	b := &backend{target: mustURL(t, "http://b.test"), label: "b"}
	c := &backend{target: mustURL(t, "http://c.test"), label: "c"}
	a.healthy.Store(true)
	b.healthy.Store(true)
	c.healthy.Store(true)

	pool := &backendPool{backends: []*backend{a, b, c}}

	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		next := pool.next(balanceRoundRobin, nil)
		if next == nil {
			t.Fatal("expected backend")
		}
		seen[next.label]++
	}
	if seen["a"] == 0 || seen["b"] == 0 || seen["c"] == 0 {
		t.Fatalf("round robin should touch all backends, got=%v", seen)
	}

	a.inflight.Store(5)
	b.inflight.Store(1)
	c.inflight.Store(3)
	least := pool.next(balanceLeastConn, nil)
	if least != b {
		t.Fatalf("expected least-conn backend b, got %s", least.label)
	}

	b.healthy.Store(false)
	excluded := map[*backend]struct{}{c: {}}
	least2 := pool.next(balanceLeastConn, excluded)
	if least2 != a {
		t.Fatalf("expected backend a after exclusions, got %v", least2)
	}
}

func TestEdgeLeastConnPolicyRoutesToLowestInflight(t *testing.T) {
	cfg := testConfig("http://a.test", "http://b.test")
	cfg.BalancePolicy = balanceLeastConn
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		return successResponse(req, http.StatusOK, req.URL.Host), nil
	}))
	edge.pool.backends[0].inflight.Store(9)
	edge.pool.backends[1].inflight.Store(1)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
	edge.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "b.test" {
		t.Fatalf("expected least-conn backend b.test, got %q", rec.Body.String())
	}
}

func TestEdgeSkipsUnhealthyBackends(t *testing.T) {
	cfg := testConfig("http://a.test", "http://b.test")
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		return successResponse(req, http.StatusOK, req.URL.Host), nil
	}))
	edge.pool.backends[0].healthy.Store(false)

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
		edge.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) != "b.test" {
			t.Fatalf("expected healthy backend b.test, got %q", rec.Body.String())
		}
	}
}

func TestEdgeRetryIdempotentRequestOnTransportError(t *testing.T) {
	cfg := testConfig("http://success.test", "http://fail.test")
	cfg.BalancePolicy = balanceRoundRobin
	cfg.RetryAttempts = 2

	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		switch req.URL.Host {
		case "fail.test":
			return nil, errors.New("dial timeout")
		case "success.test":
			return successResponse(req, http.StatusOK, "ok"), nil
		default:
			return nil, errors.New("unexpected host")
		}
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
	edge.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected retry to succeed with 200, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestEdgeCircuitBreakerOpensAndRejects(t *testing.T) {
	cfg := testConfig("http://single.test")
	cfg.CircuitFailThreshold = 2
	cfg.RetryAttempts = 1

	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		return nil, errors.New("connection refused")
	}))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
		edge.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d expected 502, got %d", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
	edge.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after circuit opens, got %d", rec.Code)
	}
}

func TestCheckBackendsRecoversUnhealthyBackend(t *testing.T) {
	cfg := testConfig("http://recover.test")
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		return successResponse(req, http.StatusOK, "ok"), nil
	}))

	b := edge.pool.backends[0]
	b.healthy.Store(false)
	b.mu.Lock()
	b.circuitOpenUntil = time.Now().Add(time.Minute)
	b.mu.Unlock()

	edge.checkBackends()

	if !b.healthy.Load() {
		t.Fatal("backend should recover after successful health check")
	}
	b.mu.RLock()
	if !b.circuitOpenUntil.IsZero() {
		t.Fatalf("circuit should be closed after recovery, got %v", b.circuitOpenUntil)
	}
	b.mu.RUnlock()
}

func TestEdgeAPIKeyAuth(t *testing.T) {
	cfg := testConfig("http://secure.test")
	cfg.APIKeys = []string{"secret"}
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		return successResponse(req, http.StatusOK, "ok"), nil
	}))

	unauthorized := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
	edge.ServeHTTP(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	authorizedReq := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
	authorizedReq.Header.Set("X-API-Key", "secret")
	edge.ServeHTTP(authorized, authorizedReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", authorized.Code)
	}
}

func TestAdminBackendsEndpointAuthAndHiddenMode(t *testing.T) {
	cfg := testConfig("http://secure.test")
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return successResponse(req, http.StatusOK, "ok"), nil
	}))

	hidden := httptest.NewRecorder()
	hiddenReq := httptest.NewRequest(http.MethodGet, "/__edge/backends", nil)
	edge.ServeHTTP(hidden, hiddenReq)
	if hidden.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when admin token disabled, got %d", hidden.Code)
	}

	edge.cfg.AdminToken = "admin-token"

	unauthorized := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/__edge/backends", nil)
	edge.ServeHTTP(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	authorizedReq := httptest.NewRequest(http.MethodGet, "/__edge/backends", nil)
	authorizedReq.Header.Set("X-Edge-Admin-Token", "admin-token")
	edge.ServeHTTP(authorized, authorizedReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", authorized.Code)
	}
	if !strings.Contains(authorized.Body.String(), "secure.test") {
		t.Fatalf("expected backend payload, got %q", authorized.Body.String())
	}
}

func TestEdgeMetricsEndpointProtectedAndRecorded(t *testing.T) {
	cfg := testConfig("http://metrics.test")
	cfg.APIKeys = []string{"secret"}
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		return successResponse(req, http.StatusOK, "ok"), nil
	}))

	unauthorizedMetrics := httptest.NewRecorder()
	unauthorizedMetricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	edge.ServeHTTP(unauthorizedMetrics, unauthorizedMetricsReq)
	if unauthorizedMetrics.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized metrics, got %d", unauthorizedMetrics.Code)
	}

	proxyReq := httptest.NewRecorder()
	proxyReqReq := httptest.NewRequest(http.MethodGet, "/v1/watermarks", nil)
	proxyReqReq.Header.Set("X-API-Key", "secret")
	edge.ServeHTTP(proxyReq, proxyReqReq)
	if proxyReq.Code != http.StatusOK {
		t.Fatalf("expected 200 from proxied request, got %d", proxyReq.Code)
	}

	metrics := httptest.NewRecorder()
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsReq.Header.Set("X-API-Key", "secret")
	edge.ServeHTTP(metrics, metricsReq)
	if metrics.Code != http.StatusOK {
		t.Fatalf("expected 200 metrics, got %d", metrics.Code)
	}

	backendLabel := edge.pool.backends[0].label
	requests := metricValue(t, metrics.Body.String(), "helios_edge_backend_requests_total", map[string]string{
		"backend":      backendLabel,
		"status_class": "2xx",
	})
	if requests < 1 {
		t.Fatalf("expected requests metric >=1, got %f", requests)
	}
	inflight := metricValue(t, metrics.Body.String(), "helios_edge_backend_inflight", map[string]string{
		"backend": backendLabel,
	})
	if inflight != 0 {
		t.Fatalf("expected inflight=0, got %f", inflight)
	}
}

func TestLoadConfigFromEnvValidation(t *testing.T) {
	t.Setenv("HELIOS_EDGE_BACKENDS", "http://a.test")
	t.Setenv("HELIOS_EDGE_BALANCE_POLICY", "least_conn")
	t.Setenv("HELIOS_EDGE_RETRY_ATTEMPTS", "2")
	cfg, err := loadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected config error: %v", err)
	}
	if cfg.BalancePolicy != balanceLeastConn {
		t.Fatalf("expected least_conn policy, got %s", cfg.BalancePolicy)
	}
	if cfg.RetryAttempts != 2 {
		t.Fatalf("expected retry attempts=2, got %d", cfg.RetryAttempts)
	}

	t.Setenv("HELIOS_EDGE_BALANCE_POLICY", "invalid")
	if _, err := loadConfigFromEnv(); err == nil {
		t.Fatal("expected error for invalid balance policy")
	}
}

func TestBackendPoolHealthyCountRespectsCircuitOpen(t *testing.T) {
	b := &backend{target: mustURL(t, "http://one.test"), label: "one"}
	b.healthy.Store(true)
	b.mu.Lock()
	b.circuitOpenUntil = time.Now().Add(5 * time.Minute)
	b.mu.Unlock()
	pool := &backendPool{backends: []*backend{b}}

	if got := pool.healthyCount(); got != 0 {
		t.Fatalf("expected 0 healthy due to open circuit, got %d", got)
	}

	b.mu.Lock()
	b.circuitOpenUntil = time.Time{}
	b.mu.Unlock()
	if got := pool.healthyCount(); got != 1 {
		t.Fatalf("expected 1 healthy, got %d", got)
	}
}

func TestEdgeRetrySkipsNonIdempotentMethods(t *testing.T) {
	cfg := testConfig("http://a.test", "http://b.test")
	cfg.RetryAttempts = 2

	attempts := atomic.Int32{}
	edge := newEdgeWithTransport(t, cfg, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/healthz" {
			return successResponse(req, http.StatusOK, "ok"), nil
		}
		attempts.Add(1)
		return nil, errors.New("transport")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/watermarks", strings.NewReader("body"))
	edge.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if attempts.Load() != 1 {
		t.Fatalf("non-idempotent request must not retry, attempts=%d", attempts.Load())
	}
}
