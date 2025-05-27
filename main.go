package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/mux"
)

// LogColors for colored logging
type LogColors struct {
	DEBUG    string
	INFO     string
	WARNING  string
	ERROR    string
	CRITICAL string
	ENDCOLOR string
}

var logColors = LogColors{
	DEBUG:    "\033[92m", // GREEN
	INFO:     "\033[94m", // BLUE
	WARNING:  "\033[93m", // YELLOW
	ERROR:    "\033[91m", // RED
	CRITICAL: "\033[91m", // RED
	ENDCOLOR: "\033[0m",  // Reset
}

// App holds the application state
type App struct {
	redisClient       *redis.Client
	accessTokens      []string
	currentTokenIdx   int64
	apiUsageThreshold int
	isDevelopment     bool
	trustedNetworks   []*net.IPNet
}

// CacheData represents cached response data
type CacheData struct {
	Data        string `json:"data"`
	ETag        string `json:"etag"`
	LastChecked int64  `json:"last_checked"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Detail  string `json:"detail,omitempty"`
	Message string `json:"message,omitempty"`
}

// PingResponse represents a ping response
type PingResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// DevConfig holds development configuration
var DevConfig = map[string]interface{}{
	"GITHUB_PATS":         "YOUR_DEV_PAT1,YOUR_DEV_PAT2",
	"API_USAGE_THRESHOLD": 50,
	"REDIS_HOST":          "localhost",
	"FORWARDED_ALLOW_IPS": "127.0.0.1/32,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16",
}

// ResponseWriter wrapper to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// NewApp creates a new application instance
func NewApp() (*App, error) {
	app := &App{}

	// Check if running in development mode
	app.isDevelopment = os.Getenv("GO_ENV") == "development"

	var githubPats string
	var apiUsageThreshold int
	var redisHost string
	var forwardedAllowIPs string

	if app.isDevelopment {
		githubPats = DevConfig["GITHUB_PATS"].(string)
		apiUsageThreshold = DevConfig["API_USAGE_THRESHOLD"].(int)
		redisHost = DevConfig["REDIS_HOST"].(string)
		forwardedAllowIPs = DevConfig["FORWARDED_ALLOW_IPS"].(string)
	} else {
		requiredVars := []string{"GITHUB_PATS", "API_USAGE_THRESHOLD", "REDIS_HOST"}
		for _, variable := range requiredVars {
			if os.Getenv(variable) == "" {
				return nil, fmt.Errorf("%s not set in environment variables", variable)
			}
		}
		githubPats = os.Getenv("GITHUB_PATS")
		var err error
		apiUsageThreshold, err = strconv.Atoi(os.Getenv("API_USAGE_THRESHOLD"))
		if err != nil {
			return nil, fmt.Errorf("invalid API_USAGE_THRESHOLD: %v", err)
		}
		redisHost = os.Getenv("REDIS_HOST")
		forwardedAllowIPs = os.Getenv("FORWARDED_ALLOW_IPS")
	}

	app.accessTokens = strings.Split(githubPats, ",")
	app.apiUsageThreshold = apiUsageThreshold

	// Parse trusted networks for X-Forwarded-For validation (supports CIDR notation)
	if forwardedAllowIPs != "" {
		networks := strings.Split(forwardedAllowIPs, ",")
		for _, network := range networks {
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

	// Initialize Redis client
	app.redisClient = redis.NewClient(&redis.Options{
		Addr:     redisHost + ":6379",
		DB:       0,
		Password: "",
	})

	// Test Redis connection
	ctx := context.Background()
	_, err := app.redisClient.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}

	return app, nil
}

// getFromCache retrieves data from Redis cache
func (app *App) getFromCache(ctx context.Context, key string) (*CacheData, error) {
	result, err := app.redisClient.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return nil, nil
	}

	lastChecked, _ := strconv.ParseInt(result["last_checked"], 10, 64)

	return &CacheData{
		Data:        result["data"],
		ETag:        result["etag"],
		LastChecked: lastChecked,
	}, nil
}

// setToCache stores data in Redis cache
func (app *App) setToCache(ctx context.Context, key string, data interface{}, etag string) error {
	currentTime := time.Now().Unix()
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	pipe := app.redisClient.Pipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"data":         string(dataJSON),
		"etag":         etag,
		"last_checked": currentTime,
	})
	pipe.Expire(ctx, key, 24*time.Hour)
	_, err = pipe.Exec(ctx)
	return err
}

// getNextToken returns the next access token in round-robin fashion
func (app *App) getNextToken() string {
	idx := atomic.AddInt64(&app.currentTokenIdx, 1) - 1
	return app.accessTokens[idx%int64(len(app.accessTokens))]
}

// logInfo logs an info message with color
func logInfo(message string) {
	timestamp := time.Now().Format("02/01/2006 15:04:05")
	fmt.Printf("%sINF%s %s %s\n", logColors.INFO, logColors.ENDCOLOR, timestamp, message)
}

// logWarning logs a warning message with color
func logWarning(message string) {
	timestamp := time.Now().Format("02/01/2006 15:04:05")
	fmt.Printf("%sWRN%s %s %s\n", logColors.WARNING, logColors.ENDCOLOR, timestamp, message)
}

// logError logs an error message with color
func logError(message string) {
	timestamp := time.Now().Format("02/01/2006 15:04:05")
	fmt.Printf("%sERR%s %s %s\n", logColors.ERROR, logColors.ENDCOLOR, timestamp, message)
}

// getClientIP extracts the client IP from request headers with CIDR subnet validation
func (app *App) getClientIP(r *http.Request) string {
	// If no trusted networks configured, don't trust forwarded headers
	if len(app.trustedNetworks) == 0 {
		return r.RemoteAddr
	}

	// Extract IP from RemoteAddr (remove port if present)
	remoteIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remoteIP); err == nil {
		remoteIP = host
	}

	// Parse the remote IP
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return r.RemoteAddr // Fallback if parsing fails
	}

	// Check if the request is coming from a trusted network
	isTrusted := false
	for _, network := range app.trustedNetworks {
		if network.Contains(ip) {
			isTrusted = true
			break
		}
	}

	// Only trust X-Forwarded-For if the request comes from a trusted network
	if isTrusted {
		forwarded := r.Header.Get("X-Forwarded-For")
		if forwarded != "" {
			// Take first IP from comma-separated list
			ips := strings.Split(forwarded, ",")
			clientIP := strings.TrimSpace(ips[0])
			if clientIP != "" {
				return clientIP
			}
		}
	}

	return r.RemoteAddr
}

// versionHandler handles the /version endpoint
func (app *App) versionHandler(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	startTime := time.Now()

	// Wrap response writer to capture status code
	wrapped := &responseWriter{ResponseWriter: w, statusCode: 200}
	w = wrapped

	w.Header().Set("Content-Type", "application/json")

	urlToFetch := r.URL.Query().Get("url")
	if urlToFetch == "" {
		ip := app.getClientIP(r)
		logWarning(fmt.Sprintf("Invalid parameter ip=%s error=url parameter is required uri=%s", ip, r.URL.Path))

		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:  "Invalid parameter",
			Detail: "url parameter is required",
		})

		// Log client request
		logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
		return
	}

	// Get cached response
	cachedResp, err := app.getFromCache(ctx, urlToFetch)
	if err != nil {
		logError(fmt.Sprintf("Cache error: %v", err))
	}

	currentTime := time.Now().Unix()
	var lastChecked int64 = 0
	if cachedResp != nil {
		lastChecked = cachedResp.LastChecked
	}

	// Get next token (round-robin)
	token := app.getNextToken()
	headers := map[string]string{
		"Authorization": fmt.Sprintf("token %s", token),
	}

	// Check if we can use cached data (within 60 seconds)
	if cachedResp != nil && cachedResp.ETag != "" {
		if currentTime-lastChecked < 60 {
			logInfo(fmt.Sprintf("Using newly cached data for URL: %s", urlToFetch))

			var data interface{}
			json.Unmarshal([]byte(cachedResp.Data), &data)
			json.NewEncoder(w).Encode(data)

			// Log client request
			ip := app.getClientIP(r)
			logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
			return
		} else {
			// Add If-None-Match header for conditional request
			headers["If-None-Match"] = cachedResp.ETag
		}
	}

	// Make HTTP request to GitHub API
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", urlToFetch, nil)
	if err != nil {
		ip := app.getClientIP(r)
		logWarning(fmt.Sprintf("Invalid request ip=%s error=%s uri=%s", ip, err.Error(), r.URL.Path))

		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:  "Invalid request",
			Detail: err.Error(),
		})

		// Log client request
		logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
		return
	}

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		ip := app.getClientIP(r)
		logWarning(fmt.Sprintf("Invalid request ip=%s error=%s uri=%s", ip, err.Error(), r.URL.Path))

		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:  "Invalid request",
			Detail: err.Error(),
		})

		// Log client request
		logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
		return
	}
	defer resp.Body.Close()

	// Log the HTTP request to upstream API
	logInfo(fmt.Sprintf("HTTP Request: GET %s \"%s %d %s\"", urlToFetch, resp.Proto, resp.StatusCode, resp.Status))

	// Get rate limit remaining
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining == "" {
		remaining = "0"
	}
	ip := app.getClientIP(r)

	// Calculate response size
	contentLength := resp.ContentLength
	if contentLength == -1 {
		contentLength = 0
	}

	// Log request details
	duration := time.Since(startTime).Seconds()
	logMsg := fmt.Sprintf("Request processed duration=%.6fs ip=%s method=GET size='%.2f KiB' status=%d uri=%s rate_limit_remaining=%s",
		duration, ip, float64(contentLength)/1024, resp.StatusCode, urlToFetch, remaining)
	logInfo(logMsg)

	// Handle response based on status code
	if resp.StatusCode == 304 {
		// Not Modified - use cached data
		if cachedResp != nil {
			logInfo(fmt.Sprintf("Using cached data for URL: %s", urlToFetch))

			var data interface{}
			json.Unmarshal([]byte(cachedResp.Data), &data)
			// Update cache with new last_checked time
			app.setToCache(ctx, urlToFetch, data, cachedResp.ETag)
			json.NewEncoder(w).Encode(data)

			// Log client request
			logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
			return
		}
	} else if resp.StatusCode == 200 {
		// Success - parse and cache response
		var data interface{}
		err = json.NewDecoder(resp.Body).Decode(&data)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:  "Failed to parse response",
				Detail: err.Error(),
			})

			// Log client request
			logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
			return
		}

		etag := resp.Header.Get("ETag")
		app.setToCache(ctx, urlToFetch, data, etag)
		json.NewEncoder(w).Encode(data)

		// Log client request
		logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
		return
	}

	// Handle error responses (4xx, 5xx, etc.)
	var errorResponseBody interface{}
	json.NewDecoder(resp.Body).Decode(&errorResponseBody)

	w.WriteHeader(resp.StatusCode)
	
	errorResponse := map[string]interface{}{
		"error": "Upstream API error",
	}

	if errorResponseBody != nil {
		// If we got structured error data, include it as "message"
		errorResponse["message"] = errorResponseBody
	} else {
		// If no structured data, include the raw response text
		errorResponse["message"] = resp.Status
	}

	json.NewEncoder(w).Encode(errorResponse)

	// Log client request
	logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
}

// pingHandler handles the /ping endpoint
func (app *App) pingHandler(w http.ResponseWriter, r *http.Request) {
	// Wrap response writer to capture status code for logging
	wrapped := &responseWriter{ResponseWriter: w, statusCode: 200}
	w = wrapped

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PingResponse{
		Status:  "ok",
		Message: "Service is up and running",
	})

	// Log client request
	ip := app.getClientIP(r)
	logInfo(fmt.Sprintf("%s - \"%s %s %s\" %d", ip, r.Method, r.URL.RequestURI(), r.Proto, wrapped.statusCode))
}

func main() {
	app, err := NewApp()
	if err != nil {
		logError(fmt.Sprintf("Failed to initialize app: %v", err))
		os.Exit(1)
	}

	router := mux.NewRouter()
	router.HandleFunc("/version", app.versionHandler).Methods("GET")
	router.HandleFunc("/ping", app.pingHandler).Methods("GET")

	port := os.Getenv("PORT")
	if port == "" {
		if app.isDevelopment {
			port = "5000"
		} else {
			port = "8000"
		}
	}

	logInfo(fmt.Sprintf("Starting server on port %s", port))

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Fatal(server.ListenAndServe())
}
