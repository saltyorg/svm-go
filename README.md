# svm-go

`svm-go` is a `fasthttp` service that resolves release/version metadata from an upstream HTTPS URL and caches responses in a bounded in-memory LRU cache.

## Endpoints

### `GET /version?url=<https-url>[&refresh=true]`

Returns upstream JSON response data for the provided URL.

Behavior:
- Accepts only valid `https` URLs.
- URL host must be in the allowlist (defaults to known GitHub API/content hosts).
- Redirect destinations are revalidated against HTTPS and the same host allowlist.
- Upstream connections reject private and special-purpose resolved addresses and non-443 ports.
- GitHub credentials are sent only to `api.github.com`.
- `ALLOWED_UPSTREAM_HOSTS` can override the default allowlist.
- Uses a direct in-memory cache lookup and coalesces concurrent misses by cache key.
- `refresh=true` bypasses cache-hit fast return and forces immediate upstream refresh.

Response headers:
- `X-Cache`: one of `HIT`, `MISS`, `REVALIDATED`, `BYPASS`.
  Forced refreshes report `BYPASS`, except conditional `304` responses report `REVALIDATED`.
- `X-Cache-Key`: normalized/hash cache key used internally.
- `X-Upstream-Status`: upstream HTTP status (or `0` when no upstream call was made).

Error payload format:

```json
{
  "error": "Invalid parameter",
  "detail": "url must be an absolute https URL"
}
```

### `GET /ping`

Returns service liveness JSON:

```json
{
  "status": "ok",
  "message": "Service is up and running"
}
```

### `GET /healthz`

Reports request-serving readiness.

Status behavior:
- `200` + `healthy`: request-serving path is ready.
- `503` + `unhealthy`: request-serving path is down.

### `GET /metrics`

Prometheus text format counters:
- `svm_cache_hits_total`
- `svm_cache_misses_total`
- `svm_revalidate_runs_total`
- `svm_upstream_requests_total`
- `svm_upstream_errors_total`
- `svm_access_log_dropped_total`

## Runtime

### Run locally

```bash
GITHUB_PATS=token1,token2 \
API_USAGE_THRESHOLD=50 \
go run ./cmd/svm
```

### Run tests

```bash
go test ./...
```

### Build container

```bash
docker build -t svm-go .
```

## Configuration

Duration values use Go duration syntax (for example `30s`, `5m`, `24h`) and also support day suffixes like `7d`.

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `PORT` | no | `8000` | HTTP listen port. |
| `GITHUB_PATS` | yes | none | Comma-separated Personal Access Tokens used for upstream requests. |
| `API_USAGE_THRESHOLD` | yes | none | Threshold used by token selection policy. |
| `FORWARDED_ALLOW_IPS` | no | empty (do not trust forwarded headers) | Comma-separated CIDRs/IPs trusted for `X-Forwarded-For`. |
| `ALLOWED_UPSTREAM_HOSTS` | no | `api.github.com,uploads.github.com,raw.githubusercontent.com,codeload.github.com,objects.githubusercontent.com,github-releases.githubusercontent.com` | Comma-separated host allowlist for `/version?url=...`. |
| `CACHE_HARD_TTL` | no | `24h` | Hard-expiry window for positive cache entries. |
| `CACHE_NEGATIVE_TTL` | no | `5m` | TTL for cached negative responses (`404`/`410`). |
| `REVALIDATE_INTERVAL` | no | `1m` | Parsed and validated for background revalidation scheduling. |
| `REVALIDATE_LOOKBACK` | no | `7d` | Parsed and validated lookback window for active-key revalidation selection. |
| `REVALIDATE_WORKERS` | no | `2` | Fixed number of background revalidation workers. |
| `REVALIDATE_PER_WORKER_RPS` | no | `1` | Parsed and validated per-worker revalidation rate cap. |
| `REFRESH_QUEUE_SIZE` | no | `256` | Max pending background revalidation jobs. |
| `CACHE_L1_MAX_GB` | no | `1.0` | L1 memory cache target size in GiB (LRU eviction). |
| `MAX_UPSTREAM_RESPONSE_BYTES` | no | `10485760` | Maximum accepted upstream response body size in bytes. |
| `SHUTDOWN_TIMEOUT` | no | `2s` | Total graceful shutdown timeout. |

## Cache semantics

- Request-serving cache is an in-memory LRU bounded by `CACHE_L1_MAX_GB`.
- Cache contents and activity metadata intentionally reset when the process restarts.
- Active-key timestamps used by background revalidation are tracked in memory.
- Positive cache entries are served on hit when they are not hard-expired (`CACHE_HARD_TTL` / `expires_at`).
- Successful `200` upstream responses are cached with payload + ETag metadata.
- `404` and `410` are negative-cached for `CACHE_NEGATIVE_TTL`.
- `401`, `403`, `429`, and `5xx` are not cached.
- ETag conditional requests are used on upstream refresh/revalidation when an existing entry has an ETag.
