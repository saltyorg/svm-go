# svm-go

`svm-go` is a `fasthttp` service that resolves release/version metadata from an upstream HTTPS URL and caches responses with an L1 memory cache plus Redis-backed lazy hydration and async write-behind persistence.

## Endpoints

### `GET /version?url=<https-url>[&refresh=true]`

Returns upstream JSON response data for the provided URL.

Behavior:
- Accepts only valid `https` URLs.
- URL host must be in the allowlist (defaults to known GitHub API/content hosts).
- `ALLOWED_UPSTREAM_HOSTS` can override the default allowlist.
- Uses cache-first lookup (`L1` memory, then Redis hydrate on miss).
- `refresh=true` bypasses cache-hit fast return and forces immediate upstream refresh.

Response headers:
- `X-Cache`: one of `HIT`, `MISS`, `REVALIDATED`, `BYPASS`.
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

Reports overall health and dependency status.

Status behavior:
- `200` + `healthy`: request-serving path is up and Redis ping succeeds.
- `200` + `degraded`: request-serving path is up, Redis ping fails.
- `503` + `unhealthy`: request-serving path is down.

### `GET /metrics`

Prometheus text format counters:
- `svm_cache_hits_total`
- `svm_cache_misses_total`
- `svm_revalidate_runs_total`
- `svm_upstream_requests_total`
- `svm_upstream_errors_total`

## Runtime

### Run locally

```bash
GITHUB_PATS=token1,token2 \
API_USAGE_THRESHOLD=50 \
REDIS_HOST=localhost \
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
| `REDIS_HOST` | yes | none | Redis host for cache persistence/indexing. |
| `FORWARDED_ALLOW_IPS` | no | empty (do not trust forwarded headers) | Comma-separated CIDRs/IPs trusted for `X-Forwarded-For`. |
| `ALLOWED_UPSTREAM_HOSTS` | no | `api.github.com,uploads.github.com,raw.githubusercontent.com,codeload.github.com,objects.githubusercontent.com,github-releases.githubusercontent.com` | Comma-separated host allowlist for `/version?url=...`. |
| `CACHE_HARD_TTL` | no | `24h` | Hard-expiry window for positive cache entries. |
| `CACHE_NEGATIVE_TTL` | no | `5m` | TTL for cached negative responses (`404`/`410`). |
| `REVALIDATE_INTERVAL` | no | `1m` | Parsed and validated for background revalidation scheduling. |
| `REVALIDATE_LOOKBACK` | no | `7d` | Parsed and validated lookback window for active-key revalidation selection. |
| `REVALIDATE_ENDPOINTS_PER_WORKER` | no | `30` | Parsed and validated worker partition target for revalidation sweeps. |
| `REVALIDATE_PER_WORKER_RPS` | no | `1` | Parsed and validated per-worker revalidation rate cap. |
| `WRITE_BEHIND_QUEUE_SIZE` | no | `256` | Max pending async cache persistence events. |
| `WRITE_BEHIND_FLUSH_INTERVAL` | no | `1s` | Write-behind flush tick interval. |
| `WRITE_BEHIND_RETRY_MAX_INTERVAL` | no | `30s` | Max exponential backoff interval for write-behind retries. |
| `WRITE_BEHIND_RETRY_MAX_AGE` | no | `5m` | Drop queued write-behind events older than this age. |
| `CACHE_L1_MAX_GB` | no | `1.0` | L1 memory cache target size in GiB (LRU eviction). |
| `SHUTDOWN_DRAIN_TIMEOUT` | no | `2s` | Max shutdown drain time for write-behind queue. |

## Cache semantics

- Primary request-serving cache is L1 in-memory cache (`LRU` bounded by `CACHE_L1_MAX_GB`).
- On L1 miss, Redis is used for lazy hydrate and backfill into L1.
- Writes update L1 on the request path and enqueue Redis persistence asynchronously (write-behind).
- Cache-hit request path does not block on Redis writes.
- Positive cache entries are served on hit when they are not hard-expired (`CACHE_HARD_TTL` / `expires_at`).
- Successful `200` upstream responses are cached with payload + ETag metadata.
- `404` and `410` are negative-cached for `CACHE_NEGATIVE_TTL`.
- `401`, `403`, `429`, and `5xx` are not cached.
- ETag conditional requests are used on upstream refresh/revalidation when an existing entry has an ETag.
