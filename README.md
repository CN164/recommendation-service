# Recommendation Service v2

> **Stack:** Go 1.26+ · PostgreSQL 15 · Redis 7 · Docker · Gin Web Framework
> **Build Time:** Follow the setup instructions below

---

## Table of Contents

1. [Setup Instructions](#setup-instructions)
2. [Architecture Overview](#architecture-overview)
3. [Design Decisions](#design-decisions)
4. [Performance Results](#performance-results)
5. [Trade-offs and Future Improvements](#trade-offs-and-future-improvements)

---

## Setup Instructions

### Prerequisites

| Tool | Version | Install |
|---|---|---|
| **Go** | 1.26+ | https://go.dev/dl/ |
| **Docker Desktop** | Latest | https://www.docker.com/products/docker-desktop |
| **Git** | Latest | https://git-scm.com/download |
| **k6** | Latest | `winget install k6` or https://k6.io/docs/get-started/installation/ |

### Verify Installation (PowerShell)

```powershell
go version             # go version go1.26 windows/amd64
docker --version       # Docker version 24.x.x
docker compose version # Docker Compose version v2.x.x
k6 version             # k6 v0.x.x
```

### Quick Start (One Command)

```bash
# Start all services + run migrations + seed database
docker compose up --build
```

This command will:
1. Build the Go application image
2. Start PostgreSQL, Redis, and the application
3. Run database migrations automatically (via main.go)
4. Seed the database if it's empty

### Manual Setup (if needed)

```powershell
# 1. Run migrations manually (if not auto-run)
# Inside the container: go run ./cmd/migrate/main.go up
# Or via psql: psql -U user -d recommendations -f migrations/001_init.up.sql

# 2. Seed database manually
docker compose exec app go run ./scripts/seed.go

# 3. Verify API is working
curl http://localhost:8080/users/1/recommendations?limit=10
curl "http://localhost:8080/recommendations/batch?page=1&limit=20"

# 4. Check server health
curl http://localhost:8080/health
```

### Environment Variables

```env
DATABASE_URL=postgresql://user:password@postgres:5432/recommendations?sslmode=disable
REDIS_URL=redis://redis:6379
SERVER_PORT=8080
CACHE_TTL_MINUTES=10
WORKER_POOL_SIZE=10
DB_MAX_CONNS=20
DB_MIN_CONNS=5
```

---

## Architecture Overview

### System Layers

| Layer | Package | Responsibility |
|---|---|---|
| **Handler** | `internal/handler` | HTTP routing, input validation, JSON response formatting |
| **Service** | `internal/service` | Business logic, cache orchestration, worker pool management |
| **Repository** | `internal/repository` | SQL queries, bulk prefetch, DB connection pool |
| **Model** | `internal/model` | Heuristic scoring algorithm, latency/failure simulation |
| **Cache** | `internal/cache` | Redis Get/Set/Delete, key building, TTL management |
| **Domain** | `internal/domain` | Shared Go structs (User, Content, WatchHistory, etc.) |

### Layered Architecture Diagram

```
┌─────────────────────────────────────────────────────────┐
│                        CLIENT                           │
│              (Browser / Mobile / k6 Test)               │
└────────────────────────────┬────────────────────────────┘
                             │ HTTP Request
                             ▼
┌─────────────────────────────────────────────────────────┐
│                    HANDLER LAYER                        │
│              internal/handler/handler.go                │
│   HTTP Routing · Input Validation · JSON Serialization  │
└────────────────────────────┬────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────┐
│                    SERVICE LAYER                        │
│           internal/service/recommendation.go            │
│  Business Logic · Cache-or-Generate · Worker Pool Mgmt  │
└──────────────────┬──────────────────────────────────────┘
                   │                    │
          ┌────────▼────────┐  ┌────────▼────────┐
          │  REPOSITORY     │  │  MODEL CLIENT   │
          │    LAYER        │  │                 │
          │internal/        │  │internal/        │
          │repository/      │  │model/scorer.go  │
          │                 │  │                 │
          │SQL queries      │  │Scoring algorithm│
          │Bulk prefetch    │  │30-50ms latency  │
          │pgx conn pool    │  │1.5% failure sim │
          └────────┬────────┘  └─────────────────┘
                   │
          ┌────────▼────────────────────────────┐
          │             DATA LAYER              │
          │                                     │
          │  ┌──────────────┐  ┌─────────────┐  │
          │  │ PostgreSQL15 │  │   Redis 7   │  │
          │  │              │  │             │  │
          │  │ users        │  │ rec:user:   │  │
          │  │ content      │  │ {id}:limit: │  │
          │  │ user_watch_  │  │ {n}         │  │
          │  │ history      │  │ TTL 10 min  │  │
          │  └──────────────┘  └─────────────┘  │
          └─────────────────────────────────────┘
```

### Single User Recommendation Flow

**Request:** `GET /users/{user_id}/recommendations?limit=10`

```
CLIENT Request
       │
       ▼
┌─────────────────────────────────────────────────┐
│  ① HANDLER                                      │
│  Validate user_id (positive int, required)      │
│  Validate limit (1-50, default 10)              │
└────────────────────────┬────────────────────────┘
                         │ service.GetRecommendations()
                         ▼
┌─────────────────────────────────────────────────┐
│  ② SERVICE                                      │
│  Build Redis key: rec:user:{id}:limit:{limit}   │
└────────────────────────┬────────────────────────┘
                         │ cache.Get(key)
                         ▼
              ┌──────────┴──────────┐
              │                     │
        CACHE HIT               CACHE MISS
              │                     │
              ▼                     ▼
┌─────────────────┐   ┌─────────────────────────────────┐
│ ③a Deserialize  │   │ ③b Generate Fresh Recommendations│
│ JSON from Redis │   │                                 │
│                 │   │  repo.GetUser(userID)           │
│ metadata:       │   │    → validate user exists       │
│  cache_hit=true │   │    → get age, country, sub_type │
│                 │   │                                 │
│                 │   │  repo.GetWatchHistory(userID)   │
│                 │   │    → content_ids watched        │
│                 │   │    → genre distribution         │
│                 │   │                                 │
│                 │   │  repo.GetCandidateContent()     │
│                 │   │    → WHERE id NOT IN (watched)  │
│                 │   │    → ORDER BY popularity DESC   │
│                 │   │    → LIMIT 100                  │
│                 │   │                                 │
│                 │   │  model.Score(candidates, user)  │
│                 │   │    → genre preference weights   │
│                 │   │    → recency factor             │
│                 │   │    → final score per item       │
│                 │   │    → 30-50ms sleep (sim)        │
│                 │   │    → 1.5% random failure → 503  │
│                 │   │                                 │
│                 │   │  Sort DESC → take top N         │
│                 │   │  cache.Set(key, TTL=10min)      │
│                 │   │  metadata: cache_hit=false      │
└────────┬────────┘   └──────────────┬──────────────────┘
         └──────────┬────────────────┘
                    │ Format JSON response
                    ▼
┌─────────────────────────────────────────────────┐
│  ④ HANDLER — Return 200 OK                      │
│  { user_id, recommendations[], metadata }       │
└─────────────────────────────────────────────────┘
```

### Batch Recommendation Flow

**Request:** `GET /recommendations/batch?page=1&limit=20`

```
CLIENT Request
       │
       ▼
┌─────────────────────────────────────────────────┐
│  ① HANDLER                                      │
│  Validate page (>=1, default 1)                 │
│  Validate limit (1-100, default 20)             │
└────────────────────────┬────────────────────────┘
                         │ service.BatchRecommendations()
                         ▼
┌─────────────────────────────────────────────────┐
│  ② REPOSITORY — Paginated User Fetch            │
│  SELECT id FROM users                           │
│  ORDER BY id                                    │
│  LIMIT $1 OFFSET $2  -- $2 = (page-1)*limit    │
│  → returns []userID                             │
└────────────────────────┬────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────┐
│  ③ SERVICE — BULK PREFETCH (avoid N+1)          │
│                                                 │
│  repo.GetUsersByIDs(userIDs)                    │
│    → SELECT * FROM users WHERE id = ANY($1)    │
│    → map[userID]User                            │
│                                                 │
│  repo.GetWatchHistoryBulk(userIDs)              │
│    → SELECT * FROM user_watch_history           │
│      WHERE user_id = ANY($1)                    │
│    → map[userID][]WatchItem                     │
│                                                 │
│  ** Both queries run ONCE for all users **      │
│     before spawning workers — not per-worker!   │
└────────────────────────┬────────────────────────┘
                         │ prefetched data passed to workers
                         ▼
┌─────────────────────────────────────────────────┐
│  ④ SERVICE — Bounded Worker Pool                │
│  Recommended: 10 goroutine workers              │
│  jobs    := make(chan int64, len(userIDs))      │
│  results := make(chan Result, len(userIDs))     │
└───┬─────────────────────────────────────────────┘
    │  Fan out — each worker receives prefetched data
    ▼
┌───────────────────────────────────────────────────────────┐
│                    WORKER POOL                            │
│  ┌────────────┐  ┌────────────┐  ┌────────────────────┐  │
│  │  Worker 1  │  │  Worker 2  │  │  Worker N (<=10)   │  │
│  │ user_id=1  │  │ user_id=2  │  │  user_id=N         │  │
│  │            │  │            │  │                    │  │
│  │ cache check│  │ cache check│  │ cache check        │  │
│  │ (hit=done) │  │ (hit=done) │  │ (hit=done)         │  │
│  │            │  │            │  │                    │  │
│  │ miss=      │  │ miss=      │  │ miss=              │  │
│  │ use prefetch│  │ use prefetch│  │ use prefetch       │  │
│  │ → score    │  │ → score    │  │ → score            │  │
│  │ → cache set│  │ → cache set│  │ → cache set        │  │
│  │ → result   │  │ → result   │  │ → result           │  │
│  └─────┬──────┘  └─────┬──────┘  └─────────┬──────────┘  │
└────────┼───────────────┼───────────────────┼─────────────┘
         └───────────────┴───────────────────┘
                         │ collect via channels
                         ▼
┌─────────────────────────────────────────────────┐
│  ⑤ RESULT AGGREGATION                           │
│  success_count, failed_count                    │
│  processing_time_ms                             │
└────────────────────────┬────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────┐
│  ⑥ HANDLER — Return 200 OK                      │
│  { page, limit, total_users,                    │
│    results[], summary, metadata }               │
└─────────────────────────────────────────────────┘
```

---

## Design Decisions

### Caching Strategy & TTL Rationale

- **Cache Key Structure:** Includes both `user_id` and `limit` because `?limit=10` and `?limit=20` return different result sets and should be cached separately
- **TTL = 10 minutes:** Watch history changes rarely within a user session. A 10-minute TTL significantly reduces database load while keeping recommendations reasonably fresh. Adjustable via `CACHE_TTL_MINUTES` environment variable
- **Cache Failure is Non-fatal:** If Redis is down, the system falls back to database-generated recommendations. A warning is logged, but no error is returned to the client
- **Invalidation Strategy:** When a user watches new content, all cached recommendation variants for that user are deleted (`rec:user:{id}:limit:*`)

### Concurrency Control Approach

- **Bounded Worker Pool:** Limited to 10 goroutines to avoid overwhelming the database connection pool (MaxConns=20)
- **Bounded Channels:** Channel buffers prevent goroutine leaks and memory buildup
- **Bulk Prefetch Before Fan-out:** The batch endpoint fetches ALL user and watch history data in just 2 database queries BEFORE spawning workers. This eliminates the N+1 problem entirely:
  - Query 1: `SELECT * FROM users WHERE id = ANY($1)` — gets all user data for the page
  - Query 2: `SELECT * FROM user_watch_history ... WHERE user_id = ANY($1)` — gets all watch history for the page
  - Workers then use in-memory maps (`userMap`, `historyMap`) for O(1) lookups, zero additional DB calls per user

### Error Handling Philosophy

- **404 Not Found:** User ID doesn't exist in the database (permanent, client should verify)
- **400 Bad Request:** Client validation error (invalid parameter format or range)
- **503 Service Unavailable:** Model simulation failure (1.5% rate) — transient, client should retry
- **500 Internal Server Error:** Unexpected database or system error — logged with context for debugging
- **Partial Batch Failure:** Individual worker errors (e.g., model failure) never stop other workers. Failed users appear as `{"status":"failed","error":"..."}` entries mixed with successful results

### Database Indexing Strategy

| Index | Query it Optimizes | Benefit |
|---|---|---|
| `idx_watch_history_composite (user_id, watched_at DESC)` | Fetch recent watch history for a user | Avoids sort after filtering |
| `idx_content_popularity (popularity_score DESC)` | Fetch candidates sorted by popularity | Efficient top-N retrieval |
| `idx_watch_history_user (user_id)` | Bulk fetch via `WHERE user_id = ANY(...)` | IndexScan for bulk prefetch |
| `idx_content_genre (genre)` | Filter candidates by genre (future use) | Enables genre-specific recommendations |
| `idx_users_country (country)` | Geographic filtering (future use) | Geo-based recommendations |
| `idx_users_subscription (subscription_type)` | Tier-based content filtering | Access control by subscription |

### Scoring Algorithm Weight Choices

| Component | Weight | Rationale |
|---|---|---|
| **Popularity** | **40%** | Strongest universal signal — popular content is broadly enjoyed. Works well for cold-start users |
| **Genre Preference** | **35%** | Core personalization — heavily weighted because it reflects actual user behavior from watch history |
| **Recency** | **15%** | Slight boost for newer content to keep recommendations fresh and prevent stale content from dominating |
| **Exploration** | **10%** | Controlled randomness [−0.005, +0.005] to prevent filter bubbles and add healthy diversity |

The formula is applied per-candidate:
```
score = (popularity × 0.40) 
      + (genre_pref × 0.35) 
      + (recency × 0.15) 
      + (random_noise × 0.10)
```

---

## Performance Results

### Test Environment

| Aspect | Value |
|---|---|
| **Machine** | Intel Core i5-13500 / 32 GB RAM |
| **Docker Resources** | Default Docker Desktop allocation |
| **Dataset** | 300 users, 50 content items, 3,000 watch history records |
| **k6 Version** | v1.6.1 |

### 1. Single User Load Test (`load_test.js`)

> Ramp: 0→100 VUs over 30s · Hold 1 min · Ramp-down 30s · User IDs 1–300

| Metric | Target | Result | Status |
|---|---|---|---|
| **Avg Latency** | <200ms | 1.30 ms | ✅ |
| **P95 Latency** | <500ms | 2.08 ms | ✅ |
| **P99 Latency** | <1000ms | 2.71 ms | ✅ |
| **Throughput** | >80 req/s | 550.8 req/s | ✅ |
| **Error Rate** | <3% | 0.01% | ✅ |
| Checks Passed | 100% | 99.99% | ✅ |

### 2. Batch Endpoint Stress Test (`batch_test.js`)

> 20 VUs · Varying page/limit combos: pages 1–3 × limits 20/50/100 · 300 users cover all combinations

| Metric | Target | Result | Status |
|---|---|---|---|
| **Avg Latency** | — | 4.96 ms | ✅ |
| **P95 Latency** | <3000ms | 7.66 ms | ✅ |
| **P99 Latency** | — | ~10 ms | ✅ |
| **Throughput** | — | 10.9 req/s | ✅ |
| **Error Rate** | <5% | 0.00% | ✅ |
| Checks Passed | 100% | 100.00% | ✅ |

### 3. Cache Effectiveness Test (`cache_test.js`)

> 10 VUs · 2 minutes · User IDs 1–5 (warm-up by repeated requests)

| Metric | Target | Result | Status |
|---|---|---|---|
| **Cache Hit Rate** | >70% | 100.00% | ✅ |
| **Avg Latency** | — | 1.28 ms | ✅ |
| **P95 Latency** | — | 2.11 ms | ✅ |
| **Latency (cache hit)** | — | avg 1.29 ms | ✅ |
| **Throughput** | — | 6,780 req/s | ✅ |
| **Error Rate** | — | 0.00% | ✅ |

### Bottleneck Analysis

- **Cache miss dominated by model simulation:** The 30–50ms sleep in `scorer.go` is the primary bottleneck on cold requests. After cache warm-up, 100% of requests are served from Redis at ~1.3ms
- **Cache eliminates model cost entirely:** 1.3ms (hit) vs ~40ms (miss) — a 30× speedup, making Redis the key performance lever
- **Load test: cache warm-up effect visible:** Early requests (cold) are slower; steady-state p95 = 2.08ms shows the cache is fully warmed after ~30s
- **Batch test: worker pool keeps latency low:** 100 users processed concurrently with 10 workers → ~400ms total, well within 3,000ms threshold
- **Redis is not a bottleneck:** 6,780 req/s at sub-2ms latency confirms Redis capacity far exceeds the application demand

---

## Trade-offs and Future Improvements

### Known Limitations

1. **Small Seed Dataset:** 20 users and 50 content items don't reflect production scale. Performance characteristics will differ significantly with 1M+ users
2. **`KEYS` Command Bottleneck:** The cache invalidation uses `KEYS` which blocks Redis. In production, use `SCAN` with cursors
3. **No Persistent Redis:** Cache is ephemeral. A Redis restart loses all entries (data loss acceptable for caching, but monitor in production)
4. **Model Failure is Pure Random:** 1.5% failure is uniformly random. Production systems use circuit breakers with state to track failures and avoid cascades
5. **Fixed Worker Pool Size:** 10 workers is hardcoded. Should be configurable via environment variable

### Scalability Considerations

- **Horizontal Scaling:** The application is stateless. Scale by running multiple instances behind a load balancer — no code changes needed
- **Database Scaling:** PostgreSQL read replicas recommended for high recommendation query load. Connection pooling via PgBouncer improves efficiency
- **Cache Scaling:** Redis Cluster or Redis Sentinel needed for HA. Single Redis instance will become bottleneck at >50k concurrent users
- **Bulk Prefetch Limits:** Fetching 500 candidate items per batch is safe for 20 users. At 10,000 users per batch, limit this to top 100 candidates

### Proposed Enhancements (Time Permitting)

1. **Circuit Breaker Pattern:** Use `github.com/sony/gobreaker` to gracefully degrade when model service fails repeatedly
2. **Prometheus Metrics:** Expose `/metrics` endpoint with:
   - Cache hit/miss rates
   - Latency histograms (p50, p95, p99)
   - Worker pool utilization
   - Database pool saturation
3. **Cursor-Based Pagination:** Replace OFFSET with stable cursors for large user tables (OFFSET becomes O(n) at large offsets)
4. **Cache Warming:** Pre-populate cache for top-N active users on startup to improve cold-start performance
5. **Rate Limiting:** Per-user endpoint rate limiting to prevent recommendation endpoint abuse
6. **Subscription Tier Filtering:** Implement proper content access rules based on subscription level
7. **Unit & Integration Tests:** Add comprehensive test suite with `testify` assertions and `pgxmock` for repository layer
8. **Graceful Degradation:** If model service is down, fall back to popularity-only recommendations without waiting for model

---

## API Reference

### GET /users/{user_id}/recommendations

Retrieve personalized recommendations for a single user.

**Parameters:**

| Name | Type | Required | Description |
|---|---|---|---|
| `user_id` | integer | Yes | User ID (must be positive) |
| `limit` | integer | No | Recommendations to return (1-50, default 10) |

**Success Response (200):**

```json
{
  "user_id": 1,
  "recommendations": [
    {
      "content_id": 10,
      "title": "Action Movie A",
      "genre": "action",
      "popularity_score": 0.87,
      "score": 1.04
    }
  ],
  "metadata": {
    "cache_hit": false,
    "generated_at": "2026-03-14T10:30:00Z",
    "total_count": 1
  }
}
```

**Error Responses:**

- `400 Bad Request` — Invalid limit
- `404 Not Found` — User doesn't exist
- `503 Service Unavailable` — Model unavailable (1.5% chance)

---

### GET /recommendations/batch

Process recommendations for multiple users (paginated).

**Parameters:**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | integer | No | Page number (≥1, default 1) |
| `limit` | integer | No | Users per page (1-100, default 20) |

**Success Response (200):**

```json
{
  "page": 1,
  "limit": 20,
  "total_users": 45,
  "results": [
    {
      "user_id": 1,
      "recommendations": [...],
      "status": "success"
    },
    {
      "user_id": 2,
      "status": "failed",
      "error": "model_inference_failed",
      "message": "Recommendation generation failed"
    }
  ],
  "summary": {
    "success_count": 19,
    "failed_count": 1,
    "processing_time_ms": 1250
  },
  "metadata": {
    "generated_at": "2026-03-14T10:30:00Z"
  }
}
```

---

## Troubleshooting

### "User not found" Error

Ensure the user ID exists:
```bash
curl http://localhost:8080/users/999/recommendations
# Should return 404 if user 999 doesn't exist
```

Verify database has seed data:
```bash
docker compose exec postgres psql -U user -d recommendations -c "SELECT COUNT(*) FROM users;"
```

### Model Temporarily Unavailable (503)

This is expected behavior. The model simulates a 1.5% failure rate. The client should retry with exponential backoff.

### Cache Hit Rate Low

Check if Redis is connected:
```bash
docker compose exec redis redis-cli PING
# Should return: PONG
```

Monitor cache keys:
```bash
docker compose exec redis redis-cli KEYS 'rec:user:*' | wc -l
# Should show number of cached recommendations
```

### Database Connection Errors

Verify PostgreSQL is healthy:
```bash
docker compose exec postgres pg_isready
# Should return: accepting connections
```

Check connection pool status in logs:
```bash
docker compose logs app | grep "Database connection"
```

---

## License

[Your License Here]

---

**Build layer by layer: Domain → Repository → Model → Cache → Service → Handler 🚀**
