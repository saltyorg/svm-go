package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	appcore "svm/internal/app"
	"svm/internal/cache"
	"svm/internal/cache/l1"
	"svm/internal/cache/l2redis"
	"svm/internal/config"
	"svm/internal/httpapi"
	"svm/internal/observability"
	versionservice "svm/internal/service/version"
	upstreamgithub "svm/internal/upstream/github"

	"github.com/redis/go-redis/v9"
	"github.com/valyala/fasthttp"
)

const requestLogQueueSize = 256

var fallbackLogger = observability.NewLogger(os.Stdout)

type requestLogEntry struct {
	clientIP   string
	method     string
	requestURI string
	proto      string
	statusCode int
}

type versionServiceHandler interface {
	Handle(ctx context.Context, request versionservice.Request) versionservice.Response
}

type refreshByKeyHandler interface {
	RefreshByKey(cacheKey string) bool
}

type hitRecorderRuntime interface {
	RecordHit(key string, hitAt time.Time) bool
	ActiveKeysSince(ctx context.Context, since time.Time, limit int) ([]string, error)
	Close()
}

type writeBehindRuntime interface {
	Enqueue(key string, record cache.Record) bool
	CloseWithDrain(timeout time.Duration)
}

type refreshQueueRuntime interface {
	Enqueue(key string) bool
	Dequeue(ctx context.Context) (string, bool)
	Close()
}

type revalidateSchedulerRuntime interface {
	Sweep(ctx context.Context) versionservice.RevalidateSweepResult
	Close()
}

type refreshWorkersRuntime interface {
	Close()
}

type gracefulShutdownServer interface {
	ShutdownWithContext(ctx context.Context) error
}

// App holds the application state
type App struct {
	redisClient         *redis.Client
	tokenProvider       upstreamgithub.TokenProvider
	port                string
	logger              *observability.Logger
	metrics             *observability.Metrics
	allowedHosts        []string
	cachePolicy         cache.Policy
	trustedNetworks     []*net.IPNet
	requestLogQueue     chan requestLogEntry
	requestLogDone      chan struct{}
	upstreamClient      *upstreamgithub.Client
	versionService      versionServiceHandler
	cacheStore          versionservice.CacheStore
	hitRecorder         hitRecorderRuntime
	writeBehind         writeBehindRuntime
	refreshQueue        refreshQueueRuntime
	revalidateScheduler revalidateSchedulerRuntime
	refreshWorkers      refreshWorkersRuntime
}

// ErrorResponse represents an error response.
type ErrorResponse = versionservice.ErrorResponse

// PingResponse represents a ping response
type PingResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// NewApp creates a new application instance
func NewApp() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	app := &App{
		tokenProvider:   upstreamgithub.NewThresholdTokenProvider(cfg.GitHubPATs, cfg.APIUsageThreshold),
		port:            cfg.Port,
		logger:          fallbackLogger,
		metrics:         observability.NewMetrics(),
		allowedHosts:    cfg.AllowedUpstreamHosts,
		cachePolicy:     cfg.CachePolicy,
		requestLogQueue: make(chan requestLogEntry, requestLogQueueSize),
		requestLogDone:  make(chan struct{}),
		upstreamClient:  upstreamgithub.NewClient(),
	}

	// Parse trusted networks for X-Forwarded-For validation (supports CIDR notation)
	forwardedAllowIPs := cfg.ForwardedAllowIPs
	if forwardedAllowIPs != "" {
		networks := strings.SplitSeq(forwardedAllowIPs, ",")
		for network := range networks {
			network = strings.TrimSpace(network)
			if network == "" {
				continue
			}

			// If no CIDR notation, assume /32 for IPv4 or /128 for IPv6
			if !strings.Contains(network, "/") {
				if strings.Contains(network, ":") {
					network += "/128" // IPv6
				} else {
					network += "/32" // IPv4
				}
			}

			_, ipnet, err := net.ParseCIDR(network)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR in FORWARDED_ALLOW_IPS: %s - %v", network, err)
			}
			app.trustedNetworks = append(app.trustedNetworks, ipnet)
		}
	}

	app.redisClient, err = l2redis.NewClient(context.Background(), cfg.RedisHost)
	if err != nil {
		return nil, err
	}
	l2Store := l2redis.NewStore(app.redisClient)
	app.hitRecorder = versionservice.NewHitRecorder(
		l2Store,
		cfg.CachePolicy.WriteBehindQueueSize,
		app.getLogger(),
	)
	app.writeBehind = versionservice.NewWriteBehind(
		l2Store,
		cfg.CachePolicy.WriteBehindQueueSize,
		cfg.CachePolicy.WriteBehindFlushInterval,
		cfg.CachePolicy.WriteBehindRetryMaxInterval,
		cfg.CachePolicy.WriteBehindRetryMaxAge,
		app.getLogger(),
	)
	app.cacheStore = cache.NewFacade(
		l1.NewCacheWithMaxGB(cfg.CachePolicy.L1MaxGB),
		l2Store,
		app.writeBehind,
	)
	app.versionService = app.newVersionService()
	app.startRevalidationRuntime()

	go app.processRequestLogs()

	return app, nil
}

type cacheAdapter struct {
	app *App
}

func (a cacheAdapter) Get(ctx context.Context, key string) (cache.Record, bool, error) {
	if a.app == nil || a.app.cacheStore == nil {
		return cache.Record{}, false, nil
	}

	return a.app.cacheStore.Get(ctx, key)
}

func (a cacheAdapter) Set(key string, record cache.Record) (bool, error) {
	if a.app == nil || a.app.cacheStore == nil {
		return false, nil
	}

	return a.app.cacheStore.Set(key, record)
}

func (app *App) canServeRequests() bool {
	return app != nil && app.upstreamClient != nil && app.tokenProvider != nil
}

func (app *App) pingRedis(ctx context.Context) error {
	if app == nil || app.redisClient == nil {
		return errors.New("redis client is not configured")
	}

	return app.redisClient.Ping(ctx).Err()
}

func (app *App) getLogger() *observability.Logger {
	if app != nil && app.logger != nil {
		return app.logger
	}

	return fallbackLogger
}

func (app *App) newVersionService() versionServiceHandler {
	service := versionservice.NewService(
		cacheAdapter{app: app},
		app.hitRecorder,
		app.tokenProvider,
		app.upstreamClient,
		app.getLogger(),
		app.allowedHosts,
		app.cachePolicy,
	)
	service.SetMetrics(app.metrics)
	return service
}

func (app *App) startRevalidationRuntime() {
	if app == nil || app.hitRecorder == nil || app.cacheStore == nil {
		return
	}

	refresher, ok := app.versionService.(refreshByKeyHandler)
	if !ok {
		return
	}

	app.refreshQueue = versionservice.NewRefreshQueue(app.cachePolicy.WriteBehindQueueSize)
	workers := versionservice.NewRefreshWorkers(
		app.refreshQueue,
		refresher,
		1,
		app.cachePolicy.RevalidatePerWorkerRPS,
		app.getLogger(),
	)
	app.refreshWorkers = workers

	scheduler := versionservice.NewRevalidateScheduler(
		app.hitRecorder,
		app.cacheStore,
		app.refreshQueue,
		app.cachePolicy,
		app.getLogger(),
	)
	scheduler.SetWorkerScaler(workers)
	scheduler.SetMetrics(app.metrics)
	app.revalidateScheduler = scheduler
}

// getClientIP extracts the client IP from request headers with CIDR subnet validation.
func (app *App) getClientIP(ctx *fasthttp.RequestCtx) string {
	remoteAddr := ""
	if addr := ctx.RemoteAddr(); addr != nil {
		remoteAddr = addr.String()
	}

	// If no trusted networks configured, don't trust forwarded headers.
	if len(app.trustedNetworks) == 0 {
		return remoteAddr
	}

	// Extract IP from RemoteAddr (remove port if present).
	remoteIP := remoteAddr
	if host, _, err := net.SplitHostPort(remoteIP); err == nil {
		remoteIP = host
	}

	// Parse the remote IP.
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return remoteAddr // Fallback if parsing fails.
	}

	// Check if the request is coming from a trusted network.
	isTrusted := false
	for _, network := range app.trustedNetworks {
		if network.Contains(ip) {
			isTrusted = true
			break
		}
	}

	// Only trust X-Forwarded-For if the request comes from a trusted network.
	if isTrusted {
		forwarded := string(ctx.Request.Header.Peek("X-Forwarded-For"))
		if forwarded != "" {
			// Take first IP from comma-separated list.
			ips := strings.Split(forwarded, ",")
			clientIP := strings.TrimSpace(ips[0])
			if clientIP != "" {
				return clientIP
			}
		}
	}

	return remoteAddr
}

func (app *App) enqueueClientRequestLog(ctx *fasthttp.RequestCtx) {
	if app.requestLogQueue == nil {
		return
	}

	statusCode := ctx.Response.StatusCode()
	if statusCode == 0 {
		statusCode = fasthttp.StatusOK
	}

	proto := string(ctx.Request.Header.Protocol())
	if proto == "" {
		proto = "HTTP/1.1"
	}

	entry := requestLogEntry{
		clientIP:   app.getClientIP(ctx),
		method:     string(ctx.Method()),
		requestURI: string(ctx.RequestURI()),
		proto:      proto,
		statusCode: statusCode,
	}

	select {
	case app.requestLogQueue <- entry:
	default:
	}
}

func (app *App) processRequestLogs() {
	if app.requestLogDone != nil {
		defer close(app.requestLogDone)
	}

	logger := app.getLogger()

	for entry := range app.requestLogQueue {
		logger.Info(
			"request complete",
			observability.String("client_ip", entry.clientIP),
			observability.String("method", entry.method),
			observability.String("request_uri", entry.requestURI),
			observability.String("protocol", entry.proto),
			observability.Int("status", entry.statusCode),
		)
	}
}

func (app *App) shutdownDrainTimeout() time.Duration {
	timeout := cache.DefaultPolicy().ShutdownDrainTimeout
	if app != nil && app.cachePolicy.ShutdownDrainTimeout > 0 {
		timeout = app.cachePolicy.ShutdownDrainTimeout
	}

	return timeout
}

func (app *App) shutdownBackground(timeout time.Duration) {
	if app == nil {
		return
	}

	appcore.CloseOnShutdown(
		app.revalidateScheduler,
		app.refreshWorkers,
		app.refreshQueue,
		app.hitRecorder,
	)
	appcore.DrainOnShutdown(timeout, app.writeBehind)
	app.shutdownRequestLogs(timeout)

	if app.redisClient != nil {
		if err := app.redisClient.Close(); err != nil {
			app.getLogger().Warn("failed to close redis client", observability.String("error", err.Error()))
		}
		app.redisClient = nil
	}
}

func (app *App) shutdownRequestLogs(timeout time.Duration) {
	if app == nil || app.requestLogQueue == nil {
		return
	}

	close(app.requestLogQueue)
	app.requestLogQueue = nil

	done := app.requestLogDone
	if done == nil {
		return
	}

	if timeout <= 0 {
		<-done
		app.requestLogDone = nil
		return
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
	case <-timer.C:
	}
	app.requestLogDone = nil
}

func (app *App) shutdown(server gracefulShutdownServer) error {
	if app == nil {
		return nil
	}

	timeout := app.shutdownDrainTimeout()
	var serverErr error

	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if err := server.ShutdownWithContext(ctx); err != nil {
			app.getLogger().Warn("graceful server shutdown failed", observability.String("error", err.Error()))
			serverErr = err
		}
	}

	app.shutdownBackground(timeout)
	return serverErr
}

func runServerWithLifecycle(
	application *App,
	server gracefulShutdownServer,
	signalCh <-chan os.Signal,
	listen func() error,
) error {
	if application == nil {
		return errors.New("application is required")
	}
	if listen == nil {
		return errors.New("listen function is required")
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- listen()
	}()

	if signalCh == nil {
		err := <-serverErrCh
		_ = application.shutdown(nil)
		return err
	}

	select {
	case sig := <-signalCh:
		signalName := "unknown"
		if sig != nil {
			signalName = sig.String()
		}
		application.getLogger().Info("received shutdown signal", observability.String("signal", signalName))

		shutdownErr := application.shutdown(server)
		listenErr := <-serverErrCh
		if listenErr != nil {
			return listenErr
		}
		return shutdownErr
	case err := <-serverErrCh:
		_ = application.shutdown(nil)
		return err
	}
}

// versionHandler handles the /version endpoint.
func (app *App) versionHandler(ctx *fasthttp.RequestCtx) {
	service := app.versionService
	if service == nil {
		service = app.newVersionService()
	}

	httpapi.HandleVersion(ctx, service, app.getClientIP)
}

// pingHandler handles the /ping endpoint.
func (app *App) pingHandler(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(PingResponse{
		Status:  "ok",
		Message: "Service is up and running",
	})
}

func main() {
	application, err := NewApp()
	if err != nil {
		fallbackLogger.Error("failed to initialize app", observability.String("error", err.Error()))
		os.Exit(1)
	}

	router := httpapi.WithAccessLog(
		httpapi.NewRouter(
			application.versionHandler,
			application.pingHandler,
			httpapi.NewHealthHandler(application.pingRedis, application.canServeRequests),
			httpapi.NewMetricsHandler(application.metrics),
		),
		application.enqueueClientRequestLog,
	)

	port := application.port

	application.getLogger().Info("starting server", observability.String("port", port))

	server := appcore.NewServer(router)
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	if err := runServerWithLifecycle(
		application,
		server,
		signalCh,
		func() error {
			return appcore.ListenAndServe(server, ":"+port)
		},
	); err != nil {
		application.getLogger().Error("server exited with error", observability.String("error", err.Error()))
		os.Exit(1)
	}
}
