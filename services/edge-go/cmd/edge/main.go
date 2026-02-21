package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	balanceRoundRobin = "round_robin"
	balanceLeastConn  = "least_conn"
)

type config struct {
	Addr                 string
	BackendTargets       []*url.URL
	BalancePolicy        string
	HealthPath           string
	HealthInterval       time.Duration
	BackendTimeout       time.Duration
	ReadTimeout          time.Duration
	ReadHeaderTimeout    time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	MaxBodyBytes         int64
	APIKeys              []string
	AdminToken           string
	CircuitFailThreshold int
	CircuitOpenFor       time.Duration
	RetryAttempts        int
}

type backend struct {
	target   *url.URL
	label    string
	healthy  atomic.Bool
	inflight atomic.Int64

	mu                  sync.RWMutex
	lastCheck           time.Time
	lastError           string
	consecutiveFailures int
	circuitOpenUntil    time.Time
}

type backendStatus struct {
	Target           string    `json:"target"`
	Healthy          bool      `json:"healthy"`
	Inflight         int64     `json:"inflight"`
	LastCheck        time.Time `json:"last_check"`
	LastError        string    `json:"last_error,omitempty"`
	CircuitOpenUntil time.Time `json:"circuit_open_until,omitempty"`
}

type backendPool struct {
	backends []*backend
	counter  atomic.Uint64
}

func (p *backendPool) next(policy string, exclude map[*backend]struct{}) *backend {
	switch policy {
	case balanceLeastConn:
		if b := p.nextLeastConn(exclude); b != nil {
			return b
		}
		return p.nextRoundRobin(exclude)
	default:
		if b := p.nextRoundRobin(exclude); b != nil {
			return b
		}
		return p.nextLeastConn(exclude)
	}
}

func (p *backendPool) nextRoundRobin(exclude map[*backend]struct{}) *backend {
	n := len(p.backends)
	if n == 0 {
		return nil
	}
	start := int(p.counter.Add(1))
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		candidate := p.backends[(start+i)%n]
		if exclude != nil {
			if _, found := exclude[candidate]; found {
				continue
			}
		}
		if candidate.isAvailable(now) {
			return candidate
		}
	}
	return nil
}

func (p *backendPool) nextLeastConn(exclude map[*backend]struct{}) *backend {
	now := time.Now().UTC()
	var selected *backend
	minInflight := int64(^uint64(0) >> 1)
	for _, candidate := range p.backends {
		if exclude != nil {
			if _, found := exclude[candidate]; found {
				continue
			}
		}
		if !candidate.isAvailable(now) {
			continue
		}
		inflight := candidate.inflight.Load()
		if selected == nil || inflight < minInflight {
			selected = candidate
			minInflight = inflight
		}
	}
	return selected
}

func (p *backendPool) healthyCount() int {
	count := 0
	now := time.Now().UTC()
	for _, b := range p.backends {
		if b.isAvailable(now) {
			count++
		}
	}
	return count
}

func (p *backendPool) snapshot() []backendStatus {
	out := make([]backendStatus, 0, len(p.backends))
	for _, b := range p.backends {
		b.mu.RLock()
		out = append(out, backendStatus{
			Target:           b.target.String(),
			Healthy:          b.healthy.Load(),
			Inflight:         b.inflight.Load(),
			LastCheck:        b.lastCheck,
			LastError:        b.lastError,
			CircuitOpenUntil: b.circuitOpenUntil,
		})
		b.mu.RUnlock()
	}
	return out
}

func (b *backend) isAvailable(now time.Time) bool {
	if !b.healthy.Load() {
		return false
	}
	b.mu.RLock()
	openUntil := b.circuitOpenUntil
	b.mu.RUnlock()
	if openUntil.IsZero() {
		return true
	}
	return now.After(openUntil)
}

type edgeMetrics struct {
	mu sync.RWMutex

	requests     map[string]map[string]uint64
	errors       map[string]map[string]uint64
	latencyCount map[string]uint64
	latencySum   map[string]float64
}

func newEdgeMetrics() *edgeMetrics {
	return &edgeMetrics{
		requests:     make(map[string]map[string]uint64),
		errors:       make(map[string]map[string]uint64),
		latencyCount: make(map[string]uint64),
		latencySum:   make(map[string]float64),
	}
}

func (m *edgeMetrics) incRequest(backend, statusClass string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.requests[backend]; !ok {
		m.requests[backend] = make(map[string]uint64)
	}
	m.requests[backend][statusClass]++
}

func (m *edgeMetrics) incError(backend, kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.errors[backend]; !ok {
		m.errors[backend] = make(map[string]uint64)
	}
	m.errors[backend][kind]++
}

func (m *edgeMetrics) observeLatency(backend string, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencyCount[backend]++
	m.latencySum[backend] += seconds
}

func (m *edgeMetrics) render(backends []*backend) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var builder strings.Builder

	builder.WriteString("# HELP helios_edge_backend_inflight In-flight requests per backend.\n")
	builder.WriteString("# TYPE helios_edge_backend_inflight gauge\n")
	for _, b := range backends {
		builder.WriteString(fmt.Sprintf("helios_edge_backend_inflight{backend=%q} %d\n", b.label, b.inflight.Load()))
	}

	builder.WriteString("# HELP helios_edge_backend_circuit_open Circuit breaker state per backend (1=open, 0=closed).\n")
	builder.WriteString("# TYPE helios_edge_backend_circuit_open gauge\n")
	now := time.Now().UTC()
	for _, b := range backends {
		b.mu.RLock()
		open := !b.circuitOpenUntil.IsZero() && now.Before(b.circuitOpenUntil)
		b.mu.RUnlock()
		if open {
			builder.WriteString(fmt.Sprintf("helios_edge_backend_circuit_open{backend=%q} 1\n", b.label))
		} else {
			builder.WriteString(fmt.Sprintf("helios_edge_backend_circuit_open{backend=%q} 0\n", b.label))
		}
	}

	builder.WriteString("# HELP helios_edge_backend_requests_total Backend requests by status class.\n")
	builder.WriteString("# TYPE helios_edge_backend_requests_total counter\n")
	requestBackends := sortedKeys(m.requests)
	for _, backend := range requestBackends {
		classes := m.requests[backend]
		for _, class := range sortedKeys(classes) {
			builder.WriteString(fmt.Sprintf("helios_edge_backend_requests_total{backend=%q,status_class=%q} %d\n", backend, class, classes[class]))
		}
	}

	builder.WriteString("# HELP helios_edge_backend_errors_total Backend errors by type.\n")
	builder.WriteString("# TYPE helios_edge_backend_errors_total counter\n")
	errorBackends := sortedKeys(m.errors)
	for _, backend := range errorBackends {
		types := m.errors[backend]
		for _, kind := range sortedKeys(types) {
			builder.WriteString(fmt.Sprintf("helios_edge_backend_errors_total{backend=%q,type=%q} %d\n", backend, kind, types[kind]))
		}
	}

	builder.WriteString("# HELP helios_edge_backend_latency_seconds_sum Backend upstream latency sum in seconds.\n")
	builder.WriteString("# TYPE helios_edge_backend_latency_seconds_sum counter\n")
	for _, backend := range sortedKeys(m.latencySum) {
		builder.WriteString(fmt.Sprintf("helios_edge_backend_latency_seconds_sum{backend=%q} %.9f\n", backend, m.latencySum[backend]))
	}

	builder.WriteString("# HELP helios_edge_backend_latency_seconds_count Backend upstream latency sample count.\n")
	builder.WriteString("# TYPE helios_edge_backend_latency_seconds_count counter\n")
	for _, backend := range sortedKeys(m.latencyCount) {
		builder.WriteString(fmt.Sprintf("helios_edge_backend_latency_seconds_count{backend=%q} %d\n", backend, m.latencyCount[backend]))
	}

	return builder.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type edgeServer struct {
	cfg            config
	pool           *backendPool
	healthClient   *http.Client
	upstreamClient *http.Client
	metrics        *edgeMetrics
}

func main() {
	loadDotEnv(".env", "../../.env")

	cfg, err := loadConfigFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	server, err := newEdgeServer(cfg)
	if err != nil {
		log.Fatalf("setup: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go server.startHealthLoop(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf(
		"edge listening on %s (backends=%d, policy=%s, edge_auth=%t)",
		cfg.Addr,
		len(cfg.BackendTargets),
		cfg.BalancePolicy,
		len(cfg.APIKeys) > 0,
	)

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newEdgeServer(cfg config) (*edgeServer, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   cfg.BackendTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: cfg.BackendTimeout,
	}

	pool := &backendPool{
		backends: make([]*backend, 0, len(cfg.BackendTargets)),
	}
	for _, target := range cfg.BackendTargets {
		b := &backend{target: target, label: target.String()}
		b.healthy.Store(true)
		pool.backends = append(pool.backends, b)
	}

	return &edgeServer{
		cfg:  cfg,
		pool: pool,
		healthClient: &http.Client{
			Timeout:   cfg.BackendTimeout,
			Transport: transport,
		},
		upstreamClient: &http.Client{
			Timeout:   cfg.BackendTimeout,
			Transport: transport,
		},
		metrics: newEdgeMetrics(),
	}, nil
}

func (s *edgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.handleHealth(w)
		return
	}
	if r.URL.Path == "/metrics" {
		s.handleMetrics(w, r)
		return
	}
	if r.URL.Path == "/__edge/backends" {
		s.handleBackends(w, r)
		return
	}
	if len(s.cfg.APIKeys) > 0 && !isAuthorizedAPIKey(extractAPIKey(r), s.cfg.APIKeys) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	s.proxyWithPolicy(w, r)
}

func (s *edgeServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.APIKeys) > 0 && !isAuthorizedAPIKey(extractAPIKey(r), s.cfg.APIKeys) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, s.metrics.render(s.pool.backends))
}

func (s *edgeServer) proxyWithPolicy(w http.ResponseWriter, r *http.Request) {
	attempts := 1
	if canRetryRequest(r) && s.cfg.RetryAttempts > 1 {
		attempts = s.cfg.RetryAttempts
	}

	excluded := make(map[*backend]struct{}, attempts)
	hadBackend := false
	for attempt := 0; attempt < attempts; attempt++ {
		backend := s.pool.next(s.cfg.BalancePolicy, excluded)
		if backend == nil {
			break
		}
		hadBackend = true
		excluded[backend] = struct{}{}

		allowRetry := attempt < attempts-1
		retry, err := s.forwardOnce(w, r, backend, allowRetry)
		if err == nil {
			return
		}
		if !retry {
			break
		}
	}

	if !hadBackend {
		http.Error(w, "no healthy upstreams", http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "upstream unavailable", http.StatusBadGateway)
}

func (s *edgeServer) forwardOnce(w http.ResponseWriter, r *http.Request, b *backend, allowRetry bool) (bool, error) {
	start := time.Now()
	b.inflight.Add(1)
	defer func() {
		b.inflight.Add(-1)
		s.metrics.observeLatency(b.label, time.Since(start).Seconds())
	}()

	upReq, err := prepareUpstreamRequest(r, b.target)
	if err != nil {
		s.recordFailure(b, err.Error(), "request_build")
		return false, err
	}

	resp, err := s.upstreamClient.Do(upReq)
	if err != nil {
		s.recordFailure(b, err.Error(), "transport")
		return allowRetry, err
	}
	defer resp.Body.Close()

	if allowRetry && isRetryableStatus(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, resp.Body)
		s.recordFailure(b, "upstream status "+strconv.Itoa(resp.StatusCode), "upstream_"+strconv.Itoa(resp.StatusCode))
		return true, errors.New("retryable upstream status")
	}

	copyResponse(w, resp)
	s.recordResponse(b, resp.StatusCode)
	return false, nil
}

func prepareUpstreamRequest(r *http.Request, target *url.URL) (*http.Request, error) {
	upstreamURL := *r.URL
	upstreamURL.Scheme = target.Scheme
	upstreamURL.Host = target.Host
	upstreamURL.Path = joinPath(target.Path, r.URL.Path)
	upstreamURL.RawPath = ""

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return nil, err
	}
	upReq.Header = cloneHeader(r.Header)
	removeHopByHopHeaders(upReq.Header)
	upReq.Host = target.Host
	upReq.Header.Set("X-Forwarded-Host", r.Host)
	if r.TLS != nil {
		upReq.Header.Set("X-Forwarded-Proto", "https")
	} else {
		upReq.Header.Set("X-Forwarded-Proto", "http")
	}
	upReq.Header.Set("X-Forwarded-For", appendForwardedFor(r.Header.Get("X-Forwarded-For"), clientIP(r)))
	return upReq, nil
}

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	removeHopByHopHeaders(resp.Header)
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *edgeServer) recordFailure(b *backend, errText, kind string) {
	now := time.Now().UTC()
	b.mu.Lock()
	b.lastCheck = now
	b.lastError = errText
	b.consecutiveFailures++
	if b.consecutiveFailures >= s.cfg.CircuitFailThreshold {
		b.circuitOpenUntil = now.Add(s.cfg.CircuitOpenFor)
		b.healthy.Store(false)
	}
	b.mu.Unlock()

	s.metrics.incError(b.label, kind)
}

func (s *edgeServer) recordResponse(b *backend, statusCode int) {
	s.metrics.incRequest(b.label, statusClass(statusCode))
	if statusCode >= 500 {
		s.recordFailure(b, "upstream status "+strconv.Itoa(statusCode), "upstream_5xx")
		return
	}
	now := time.Now().UTC()
	b.mu.Lock()
	b.lastCheck = now
	b.lastError = ""
	b.consecutiveFailures = 0
	b.circuitOpenUntil = time.Time{}
	b.healthy.Store(true)
	b.mu.Unlock()
}

func (s *edgeServer) handleHealth(w http.ResponseWriter) {
	healthy := s.pool.healthyCount()
	status := http.StatusOK
	if healthy == 0 {
		status = http.StatusServiceUnavailable
	}
	resp := map[string]any{
		"status":           http.StatusText(status),
		"healthy_backends": healthy,
		"total_backends":   len(s.pool.backends),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *edgeServer) handleBackends(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.cfg.AdminToken) == "" {
		http.NotFound(w, r)
		return
	}
	if !isAuthorizedAdminToken(extractAdminToken(r), s.cfg.AdminToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.pool.snapshot())
}

func (s *edgeServer) startHealthLoop(ctx context.Context) {
	s.checkBackends()
	ticker := time.NewTicker(s.cfg.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkBackends()
		}
	}
}

func (s *edgeServer) checkBackends() {
	var wg sync.WaitGroup
	for _, b := range s.pool.backends {
		wg.Add(1)
		go func(b *backend) {
			defer wg.Done()
			s.checkBackend(b)
		}(b)
	}
	wg.Wait()
}

func (s *edgeServer) checkBackend(b *backend) {
	probeURL := b.target.ResolveReference(&url.URL{Path: s.cfg.HealthPath})
	req, err := http.NewRequest(http.MethodGet, probeURL.String(), nil)
	if err != nil {
		b.setHealth(false, err.Error())
		return
	}
	resp, err := s.healthClient.Do(req)
	if err != nil {
		b.setHealth(false, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		b.setHealth(true, "")
		return
	}
	b.setHealth(false, "health status "+strconv.Itoa(resp.StatusCode))
}

func (b *backend) setHealth(healthy bool, errText string) {
	b.healthy.Store(healthy)
	b.mu.Lock()
	b.lastCheck = time.Now().UTC()
	b.lastError = errText
	if healthy {
		b.consecutiveFailures = 0
		b.circuitOpenUntil = time.Time{}
	}
	b.mu.Unlock()
}

func loadConfigFromEnv() (config, error) {
	var cfg config
	var err error

	cfg.Addr = getenv("HELIOS_EDGE_ADDR", ":8080")
	cfg.BackendTargets, err = parseBackendTargets(os.Getenv("HELIOS_EDGE_BACKENDS"))
	if err != nil {
		return cfg, err
	}
	cfg.BalancePolicy = strings.ToLower(getenv("HELIOS_EDGE_BALANCE_POLICY", balanceRoundRobin))
	if cfg.BalancePolicy != balanceRoundRobin && cfg.BalancePolicy != balanceLeastConn {
		return cfg, errors.New("HELIOS_EDGE_BALANCE_POLICY must be round_robin or least_conn")
	}
	cfg.HealthPath = getenv("HELIOS_EDGE_HEALTH_PATH", "/healthz")
	cfg.HealthInterval, err = getenvDuration("HELIOS_EDGE_HEALTH_INTERVAL", 5*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.BackendTimeout, err = getenvDuration("HELIOS_EDGE_BACKEND_TIMEOUT", 5*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.ReadTimeout, err = getenvDuration("HELIOS_EDGE_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.ReadHeaderTimeout, err = getenvDuration("HELIOS_EDGE_READ_HEADER_TIMEOUT", 5*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.WriteTimeout, err = getenvDuration("HELIOS_EDGE_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.IdleTimeout, err = getenvDuration("HELIOS_EDGE_IDLE_TIMEOUT", 120*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.MaxBodyBytes, err = getenvInt64("HELIOS_EDGE_MAX_BODY_BYTES", 1_048_576)
	if err != nil {
		return cfg, err
	}
	cfg.CircuitFailThreshold, err = getenvInt("HELIOS_EDGE_CIRCUIT_FAIL_THRESHOLD", 5)
	if err != nil {
		return cfg, err
	}
	cfg.CircuitOpenFor, err = getenvDuration("HELIOS_EDGE_CIRCUIT_OPEN_FOR", 30*time.Second)
	if err != nil {
		return cfg, err
	}
	cfg.RetryAttempts, err = getenvInt("HELIOS_EDGE_RETRY_ATTEMPTS", 2)
	if err != nil {
		return cfg, err
	}
	if cfg.RetryAttempts < 1 {
		return cfg, errors.New("HELIOS_EDGE_RETRY_ATTEMPTS must be >= 1")
	}
	cfg.APIKeys = splitCSV(os.Getenv("HELIOS_EDGE_API_KEYS"))
	cfg.AdminToken = strings.TrimSpace(os.Getenv("HELIOS_EDGE_ADMIN_TOKEN"))
	return cfg, nil
}

func parseBackendTargets(raw string) ([]*url.URL, error) {
	items := splitCSV(raw)
	if len(items) == 0 {
		return nil, errors.New("HELIOS_EDGE_BACKENDS is required")
	}
	out := make([]*url.URL, 0, len(items))
	for _, item := range items {
		u, err := url.Parse(item)
		if err != nil {
			return nil, errors.New("invalid backend URL: " + item)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, errors.New("backend URL must use http/https: " + item)
		}
		if strings.TrimSpace(u.Host) == "" {
			return nil, errors.New("backend URL host is required: " + item)
		}
		if strings.TrimSpace(u.RawQuery) != "" || strings.TrimSpace(u.Fragment) != "" {
			return nil, errors.New("backend URL must not include query/fragment: " + item)
		}
		out = append(out, u)
	}
	return out, nil
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

func extractAdminToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Edge-Admin-Token")); v != "" {
		return v
	}
	return extractAPIKey(r)
}

func isAuthorizedAPIKey(provided string, keys []string) bool {
	p := []byte(strings.TrimSpace(provided))
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

func isAuthorizedAdminToken(provided, expected string) bool {
	p := []byte(strings.TrimSpace(provided))
	e := []byte(strings.TrimSpace(expected))
	return len(p) == len(e) && len(e) > 0 && subtle.ConstantTimeCompare(p, e) == 1
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

func getenvInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, errors.New(key + ": invalid int")
	}
	return v, nil
}

func getenvInt64(key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, errors.New(key + ": invalid int64")
	}
	return v, nil
}

func loadDotEnv(paths ...string) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, exists := os.LookupEnv(key); exists {
				continue
			}
			_ = os.Setenv(key, strings.TrimSpace(value))
		}
	}
}

func joinPath(base, path string) string {
	switch {
	case base == "":
		return path
	case path == "":
		return base
	case strings.HasSuffix(base, "/") && strings.HasPrefix(path, "/"):
		return base + strings.TrimPrefix(path, "/")
	case !strings.HasSuffix(base, "/") && !strings.HasPrefix(path, "/"):
		return base + "/" + path
	default:
		return base + path
	}
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, values := range h {
		copied := make([]string, len(values))
		copy(copied, values)
		out[k] = copied
	}
	return out
}

func removeHopByHopHeaders(h http.Header) {
	hop := []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, key := range hop {
		h.Del(key)
	}
}

func appendForwardedFor(existing, ip string) string {
	existing = strings.TrimSpace(existing)
	ip = strings.TrimSpace(ip)
	if existing == "" {
		return ip
	}
	if ip == "" {
		return existing
	}
	return existing + ", " + ip
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

func isRetryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func canRetryRequest(r *http.Request) bool {
	if !isIdempotentMethod(r.Method) {
		return false
	}
	if r.ContentLength > 0 {
		return false
	}
	return true
}

func isIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func statusClass(statusCode int) string {
	switch {
	case statusCode >= 100 && statusCode < 200:
		return "1xx"
	case statusCode >= 200 && statusCode < 300:
		return "2xx"
	case statusCode >= 300 && statusCode < 400:
		return "3xx"
	case statusCode >= 400 && statusCode < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
