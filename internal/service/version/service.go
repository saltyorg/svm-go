package version

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"svm/internal/cache"
	"svm/internal/observability"
	"svm/internal/security"
	upstreamgithub "svm/internal/upstream/github"
)

// ErrorResponse represents an error payload returned by the service.
type ErrorResponse struct {
	Error   string `json:"error"`
	Detail  string `json:"detail,omitempty"`
	Message any    `json:"message,omitempty"`
}

// Request is the input envelope for version resolution.
type Request struct {
	RawURL   string
	ClientIP string
	URI      string
	// ForceRefresh bypasses the cache-hit fast-return path and forces an upstream refresh.
	ForceRefresh bool
}

// Response is the output envelope for version resolution.
type Response struct {
	StatusCode     int
	Body           any
	CacheStatus    string
	CacheKey       string
	UpstreamStatus int
}

const (
	CacheStatusBypass      = "BYPASS"
	CacheStatusHit         = "HIT"
	CacheStatusMiss        = "MISS"
	CacheStatusRevalidated = "REVALIDATED"
)

// CacheStore defines cache interactions used by the service.
type CacheStore interface {
	Get(ctx context.Context, key string) (cache.Record, bool, error)
	Set(key string, record cache.Record) (bool, error)
}

type readonlyCacheStore interface {
	GetReadonly(ctx context.Context, key string) (cache.Record, bool, error)
}

// HitSignalRecorder defines non-blocking hit signal behavior for active-key tracking.
type HitSignalRecorder interface {
	RecordHit(key string, hitAt time.Time) bool
}

// UpstreamClient defines outbound client behavior used by the service.
type UpstreamClient interface {
	Get(ctx context.Context, url, token, etag string) (*http.Response, error)
	ObserveRateLimit(token string, response *http.Response, observer upstreamgithub.RateLimitedTokenObserver)
}

type tokenRemainingObserver interface {
	ObserveTokenRemaining(token string, remaining int)
}

// Service owns the /version business flow.
type Service struct {
	cache          CacheStore
	hitRecorder    HitSignalRecorder
	tokenProvider  upstreamgithub.TokenProvider
	upstream       UpstreamClient
	logger         *observability.Logger
	metrics        *observability.Metrics
	allowedHosts   []string
	allowedHostSet map[string]struct{}
	policy         cache.Policy
	now            func() time.Time
	refreshGroup   *backgroundRefreshGroup
	requestPrep    *requestPrepCache
}

// NewService builds a version service with safe defaults for optional dependencies.
func NewService(
	cacheStore CacheStore,
	hitRecorder HitSignalRecorder,
	tokenProvider upstreamgithub.TokenProvider,
	upstream UpstreamClient,
	logger *observability.Logger,
	allowedHosts []string,
	policy cache.Policy,
) *Service {
	if tokenProvider == nil {
		tokenProvider = upstreamgithub.NewThresholdTokenProvider(nil, 0)
	}
	if upstream == nil {
		upstream = upstreamgithub.NewClient()
	}
	if logger == nil {
		logger = observability.NewLogger(io.Discard)
	}
	defaultPolicy := cache.DefaultPolicy()
	if policy.NegativeTTL <= 0 {
		policy.NegativeTTL = defaultPolicy.NegativeTTL
	}

	return &Service{
		cache:          cacheStore,
		hitRecorder:    hitRecorder,
		tokenProvider:  tokenProvider,
		upstream:       upstream,
		logger:         logger,
		allowedHosts:   append([]string(nil), allowedHosts...),
		allowedHostSet: buildAllowedHostSet(allowedHosts),
		policy:         policy,
		now:            time.Now,
		refreshGroup:   newBackgroundRefreshGroup(),
		requestPrep:    newRequestPrepCache(0),
	}
}

// SetMetrics attaches optional runtime metrics counters.
func (s *Service) SetMetrics(metrics *observability.Metrics) {
	if s == nil {
		return
	}
	s.metrics = metrics
}

// Handle resolves a version payload from cache or upstream and returns a response envelope.
func (s *Service) Handle(ctx context.Context, request Request) Response {
	prepared := s.prepareRequest(request.RawURL)
	err := prepared.err
	if err != nil {
		s.logger.Warn(
			"invalid parameter",
			observability.String("ip", request.ClientIP),
			observability.String("error", err.Error()),
			observability.String("uri", request.URI),
		)

		return Response{
			StatusCode: http.StatusBadRequest,
			Body: ErrorResponse{
				Error:  "Invalid parameter",
				Detail: err.Error(),
			},
			CacheStatus: CacheStatusBypass,
		}
	}
	urlToFetch := prepared.urlToFetch
	cacheKey := prepared.cacheKey

	cachedRecord, cacheHit, err := s.getFromCache(ctx, cacheKey)
	if err != nil {
		s.logger.Error("cache error", observability.String("error", err.Error()))
	}

	now := s.now().UTC()
	validCacheHit := cacheHit
	if cacheHit && !isNegativeCacheableStatus(cachedRecord.SourceStatus) &&
		isHardExpired(cachedRecord, now, s.policy.HardTTL) {
		validCacheHit = false
	}

	if validCacheHit {
		if !request.ForceRefresh {
			if shouldServeNegativeCachedRecord(cachedRecord, now) {
				s.recordHit(cacheKey, now)
				s.incCacheHit()

				return Response{
					StatusCode:  cachedRecord.SourceStatus,
					Body:        payloadResponseBody(cachedRecord.Payload),
					CacheStatus: CacheStatusHit,
					CacheKey:    cacheKey,
				}
			}

			if !isNegativeCacheableStatus(cachedRecord.SourceStatus) {
				s.recordHit(cacheKey, now)
				s.incCacheHit()

				return Response{
					StatusCode:  http.StatusOK,
					Body:        payloadResponseBody(cachedRecord.Payload),
					CacheStatus: CacheStatusHit,
					CacheKey:    cacheKey,
				}
			}
		}

	}

	etag := ""
	if validCacheHit && !isNegativeCacheableStatus(cachedRecord.SourceStatus) {
		etag = cachedRecord.ETag
	}

	token := s.tokenProvider.NextToken()
	startedAt := s.now()

	s.incCacheMiss()
	s.incUpstreamRequest()
	resp, err := s.upstream.Get(ctx, urlToFetch, token, etag)
	if err != nil {
		s.incUpstreamError()
		s.logger.Warn(
			"invalid upstream request",
			observability.String("ip", request.ClientIP),
			observability.String("error", err.Error()),
			observability.String("uri", request.URI),
		)

		return Response{
			StatusCode: http.StatusBadRequest,
			Body: ErrorResponse{
				Error:  "Invalid request",
				Detail: err.Error(),
			},
			CacheStatus: CacheStatusBypass,
			CacheKey:    cacheKey,
		}
	}
	defer resp.Body.Close()

	remainingHeader := resp.Header.Get("X-RateLimit-Remaining")
	remaining := remainingHeader
	if remaining == "" {
		remaining = "unknown"
	} else if usageObserver, ok := s.tokenProvider.(tokenRemainingObserver); ok {
		if remainingValue, convErr := strconv.Atoi(remainingHeader); convErr == nil {
			usageObserver.ObserveTokenRemaining(token, remainingValue)
		}
	}
	if cooldownObserver, ok := s.tokenProvider.(upstreamgithub.RateLimitedTokenObserver); ok {
		s.upstream.ObserveRateLimit(token, resp, cooldownObserver)
	}

	contentLength := resp.ContentLength
	if contentLength == -1 {
		contentLength = 0
	}

	s.logger.Info(
		"request processed",
		observability.Float64("duration_seconds", time.Since(startedAt).Seconds()),
		observability.String("ip", request.ClientIP),
		observability.String("method", http.MethodGet),
		observability.Float64("response_size_kib", float64(contentLength)/1024),
		observability.Int("status", resp.StatusCode),
		observability.String("uri", urlToFetch),
		observability.String("rate_limit_remaining", remaining),
	)

	if resp.StatusCode == http.StatusNotModified {
		if validCacheHit {
			s.logger.Info("using cached data after 304", observability.String("cache_key", cacheKey))
			s.recordHit(cacheKey, now)

			cachedRecord.LastCheckedAt = now
			cachedRecord.ExpiresAt = hardExpiresAt(now, s.policy.HardTTL)
			cachedRecord.URL = urlToFetch
			_, _ = s.setToCache(cacheKey, cachedRecord)

			return Response{
				StatusCode:     http.StatusOK,
				Body:           payloadResponseBody(cachedRecord.Payload),
				CacheStatus:    CacheStatusRevalidated,
				CacheKey:       cacheKey,
				UpstreamStatus: resp.StatusCode,
			}
		}
	} else if resp.StatusCode == http.StatusOK {
		payload, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return Response{
				StatusCode: http.StatusInternalServerError,
				Body: ErrorResponse{
					Error:  "Failed to parse response",
					Detail: readErr.Error(),
				},
				CacheStatus:    CacheStatusMiss,
				CacheKey:       cacheKey,
				UpstreamStatus: resp.StatusCode,
			}
		}
		if !json.Valid(payload) {
			return Response{
				StatusCode: http.StatusInternalServerError,
				Body: ErrorResponse{
					Error:  "Failed to parse response",
					Detail: "invalid JSON from upstream",
				},
				CacheStatus:    CacheStatusMiss,
				CacheKey:       cacheKey,
				UpstreamStatus: resp.StatusCode,
			}
		}

		record := cache.Record{
			Payload:       append([]byte(nil), payload...),
			ETag:          resp.Header.Get("ETag"),
			URL:           urlToFetch,
			FetchedAt:     now,
			LastHitAt:     now,
			LastCheckedAt: now,
			ExpiresAt:     hardExpiresAt(now, s.policy.HardTTL),
			SourceStatus:  resp.StatusCode,
		}
		_, _ = s.setToCache(cacheKey, record)

		return Response{
			StatusCode:     http.StatusOK,
			Body:           payloadResponseBody(payload),
			CacheStatus:    CacheStatusMiss,
			CacheKey:       cacheKey,
			UpstreamStatus: resp.StatusCode,
		}
	}
	if resp.StatusCode >= http.StatusBadRequest {
		s.incUpstreamError()
	}

	responseBodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		responseBodyBytes = nil
	}

	var errorResponseBody any
	if len(responseBodyBytes) > 0 {
		if err := json.Unmarshal(responseBodyBytes, &errorResponseBody); err != nil {
			errorResponseBody = nil
		}
	}

	errorResponse := map[string]any{
		"error": "Upstream API error",
	}
	if errorResponseBody != nil {
		errorResponse["message"] = errorResponseBody
	} else {
		errorResponse["message"] = resp.Status
	}
	if shouldBypassCacheWrite(resp.StatusCode) {
		return Response{
			StatusCode:     resp.StatusCode,
			Body:           errorResponse,
			CacheStatus:    CacheStatusMiss,
			CacheKey:       cacheKey,
			UpstreamStatus: resp.StatusCode,
		}
	}

	if isNegativeCacheableStatus(resp.StatusCode) {
		payload, marshalErr := json.Marshal(errorResponse)
		if marshalErr == nil {
			record := cache.Record{
				Payload:       payload,
				URL:           urlToFetch,
				FetchedAt:     now,
				LastHitAt:     now,
				LastCheckedAt: now,
				ExpiresAt:     now.Add(s.policy.NegativeTTL),
				SourceStatus:  resp.StatusCode,
			}
			_, _ = s.setToCache(cacheKey, record)
		}
	}

	return Response{
		StatusCode:     resp.StatusCode,
		Body:           errorResponse,
		CacheStatus:    CacheStatusMiss,
		CacheKey:       cacheKey,
		UpstreamStatus: resp.StatusCode,
	}
}

func (s *Service) prepareRequest(rawURL string) preparedRequest {
	if s == nil || s.requestPrep == nil {
		return prepareRequest(rawURL, nil)
	}

	return s.requestPrep.GetOrPrepare(rawURL, func(value string) preparedRequest {
		return prepareRequest(value, s.isAllowedHost)
	})
}

func prepareRequest(rawURL string, hostAllowed func(host string) bool) preparedRequest {
	parsedURL, err := security.ParseHTTPSURL(rawURL)
	if err != nil {
		return preparedRequest{err: err}
	}

	if hostAllowed != nil && !hostAllowed(parsedURL.Hostname()) {
		return preparedRequest{err: security.ErrUpstreamHostNotAllowed}
	}

	urlToFetch := parsedURL.String()
	cacheKey, keyErr := cache.KeyFromParsedURL(parsedURL)
	if keyErr != nil {
		cacheKey = urlToFetch
	}

	return preparedRequest{
		urlToFetch: urlToFetch,
		cacheKey:   cacheKey,
	}
}

// RefreshInBackground schedules an async conditional refresh deduped by cache key.
func (s *Service) RefreshInBackground(request Request) bool {
	if s == nil {
		return false
	}

	parsedURL, err := security.ParseHTTPSURL(request.RawURL)
	if err != nil {
		return false
	}
	if !s.isAllowedHost(parsedURL.Hostname()) {
		return false
	}
	urlToFetch := parsedURL.String()
	cacheKey, keyErr := cache.KeyFromParsedURL(parsedURL)
	if keyErr != nil {
		cacheKey = urlToFetch
	}

	return s.refreshGroup.Do(cacheKey, func() {
		s.refreshOnce(urlToFetch, cacheKey)
	})
}

// RefreshByKey schedules an async conditional refresh deduped by cache key.
func (s *Service) RefreshByKey(cacheKey string) bool {
	if s == nil || cacheKey == "" {
		return false
	}

	return s.refreshGroup.Do(cacheKey, func() {
		s.refreshOnceByKey(cacheKey)
	})
}

func (s *Service) getFromCache(ctx context.Context, key string) (cache.Record, bool, error) {
	if s == nil || s.cache == nil {
		return cache.Record{}, false, nil
	}
	if readonly, ok := s.cache.(readonlyCacheStore); ok {
		return readonly.GetReadonly(ctx, key)
	}

	return s.cache.Get(ctx, key)
}

func (s *Service) setToCache(key string, record cache.Record) (bool, error) {
	if s == nil || s.cache == nil {
		return false, nil
	}

	return s.cache.Set(key, record)
}

func (s *Service) recordHit(key string, hitAt time.Time) {
	if s == nil || s.hitRecorder == nil || key == "" {
		return
	}
	_ = s.hitRecorder.RecordHit(key, hitAt)
}

func (s *Service) incCacheHit() {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.IncCacheHit()
}

func (s *Service) incCacheMiss() {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.IncCacheMiss()
}

func (s *Service) incUpstreamRequest() {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.IncUpstreamRequest()
}

func (s *Service) incUpstreamError() {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.IncUpstreamError()
}

func payloadResponseBody(payload []byte) json.RawMessage {
	return json.RawMessage(payload)
}

func isNegativeCacheableStatus(statusCode int) bool {
	return statusCode == http.StatusNotFound || statusCode == http.StatusGone
}

func shouldServeNegativeCachedRecord(record cache.Record, now time.Time) bool {
	return isNegativeCacheableStatus(record.SourceStatus) &&
		!record.ExpiresAt.IsZero() &&
		record.ExpiresAt.After(now)
}

func isHardExpired(record cache.Record, now time.Time, hardTTL time.Duration) bool {
	if hardTTL <= 0 {
		return false
	}

	if !record.ExpiresAt.IsZero() {
		return !record.ExpiresAt.After(now)
	}

	// Backward compatibility for older records that do not have ExpiresAt.
	base := record.FetchedAt
	if base.IsZero() {
		base = record.LastCheckedAt
	}
	if base.IsZero() {
		return false
	}

	return !base.Add(hardTTL).After(now)
}

func hardExpiresAt(now time.Time, hardTTL time.Duration) time.Time {
	if hardTTL <= 0 {
		return time.Time{}
	}

	return now.Add(hardTTL)
}

func shouldBypassCacheWrite(statusCode int) bool {
	switch {
	case statusCode == http.StatusUnauthorized:
		return true
	case statusCode == http.StatusForbidden:
		return true
	case statusCode == http.StatusTooManyRequests:
		return true
	case statusCode >= http.StatusInternalServerError:
		return true
	default:
		return false
	}
}

func (s *Service) refreshOnce(urlToFetch, cacheKey string) {
	record, cacheHit, err := s.getFromCache(context.Background(), cacheKey)
	if err != nil {
		s.logger.Error("cache error", observability.String("error", err.Error()))
		return
	}
	if !cacheHit {
		return
	}

	s.refreshCachedRecord(cacheKey, urlToFetch, record)
}

func (s *Service) refreshOnceByKey(cacheKey string) {
	record, cacheHit, err := s.getFromCache(context.Background(), cacheKey)
	if err != nil {
		s.logger.Error("cache error", observability.String("error", err.Error()))
		return
	}
	if !cacheHit {
		return
	}
	if strings.TrimSpace(record.URL) == "" {
		return
	}

	s.refreshCachedRecord(cacheKey, record.URL, record)
}

func (s *Service) refreshCachedRecord(cacheKey, urlToFetch string, record cache.Record) {
	if isNegativeCacheableStatus(record.SourceStatus) {
		return
	}

	now := s.now().UTC()
	token := s.tokenProvider.NextToken()

	s.incUpstreamRequest()
	resp, err := s.upstream.Get(context.Background(), urlToFetch, token, record.ETag)
	if err != nil {
		s.incUpstreamError()
		s.logger.Warn("background refresh failed", observability.String("error", err.Error()))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		s.incUpstreamError()
	}

	if cooldownObserver, ok := s.tokenProvider.(upstreamgithub.RateLimitedTokenObserver); ok {
		s.upstream.ObserveRateLimit(token, resp, cooldownObserver)
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		record.LastCheckedAt = now
		record.ExpiresAt = hardExpiresAt(now, s.policy.HardTTL)
		record.URL = urlToFetch
		_, _ = s.setToCache(cacheKey, record)
	case http.StatusOK:
		payload, readErr := io.ReadAll(resp.Body)
		if readErr != nil || !json.Valid(payload) {
			return
		}

		updated := cache.Record{
			Payload:       append([]byte(nil), payload...),
			ETag:          resp.Header.Get("ETag"),
			URL:           urlToFetch,
			FetchedAt:     now,
			LastHitAt:     now,
			LastCheckedAt: now,
			ExpiresAt:     hardExpiresAt(now, s.policy.HardTTL),
			SourceStatus:  resp.StatusCode,
		}
		_, _ = s.setToCache(cacheKey, updated)
	}
}

func buildAllowedHostSet(allowedHosts []string) map[string]struct{} {
	if len(allowedHosts) == 0 {
		return nil
	}

	hostSet := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		normalized := strings.ToLower(strings.TrimSpace(host))
		if normalized == "" {
			continue
		}
		hostSet[normalized] = struct{}{}
	}
	if len(hostSet) == 0 {
		return nil
	}

	return hostSet
}

func (s *Service) isAllowedHost(host string) bool {
	if len(s.allowedHostSet) == 0 {
		return true
	}

	normalizedHost := strings.ToLower(strings.TrimSpace(host))
	if normalizedHost == "" {
		return false
	}

	_, ok := s.allowedHostSet[normalizedHost]
	return ok
}

type backgroundRefreshGroup struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func newBackgroundRefreshGroup() *backgroundRefreshGroup {
	return &backgroundRefreshGroup{
		inFlight: make(map[string]struct{}),
	}
}

func (g *backgroundRefreshGroup) Do(key string, fn func()) bool {
	if g == nil || key == "" || fn == nil {
		return false
	}

	g.mu.Lock()
	if _, exists := g.inFlight[key]; exists {
		g.mu.Unlock()
		return false
	}
	g.inFlight[key] = struct{}{}
	g.mu.Unlock()

	go func() {
		defer g.done(key)
		fn()
	}()

	return true
}

func (g *backgroundRefreshGroup) done(key string) {
	g.mu.Lock()
	delete(g.inFlight, key)
	g.mu.Unlock()
}
