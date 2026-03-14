# 🎬 Recommendation Service — Solution Plan

> **Stack:** Go 1.26+ · PostgreSQL 15 · Redis 7 · Docker · Windows 11
> **Expected Time:** 4–6 hours

---

## Table of Contents

1. [Project Structure](#1-project-structure)
2. [System Architecture & Flow Diagrams](#2-system-architecture--flow-diagrams)
3. [Critical Implementation Notes](#3-critical-implementation-notes)
4. [Database Schema Design](#4-database-schema-design)
5. [Recommendation Model & Scoring Algorithm](#5-recommendation-model--scoring-algorithm)
6. [Content Filtering Rules](#6-content-filtering-rules)
7. [API Endpoint Specifications](#7-api-endpoint-specifications)
8. [Caching Strategy](#8-caching-strategy)
9. [Data Seeding](#9-data-seeding)
10. [Docker Configuration](#10-docker-configuration)
11. [Performance Testing (k6)](#11-performance-testing-k6)
12. [Go Dependencies](#12-go-dependencies)
13. [Windows 11 Setup Guide](#13-windows-11-setup-guide)
14. [Migration & Seed File Patterns](#14-migration--seed-file-patterns)
15. [Git Commit Strategy](#15-git-commit-strategy)
16. [README.md Draft Template](#16-readmemd-draft-template)
17. [Deliverables Checklist](#17-deliverables-checklist)
18. [Evaluation Criteria](#18-evaluation-criteria)

---

## 1. Project Structure

ใช้ Standard Go Project Layout พร้อม separation of concerns ตาม assignment spec:

```
recommendation-service/
├── cmd/
│   └── server/
│       └── main.go                  # Entry point — wire dependencies & start HTTP server
├── internal/
│   ├── handler/
│   │   └── handler.go               # Handler Layer: HTTP routing, validation, JSON response
│   ├── service/
│   │   └── recommendation.go        # Service Layer: business logic, orchestration, worker pool
│   ├── repository/
│   │   ├── user.go                  # GetUserByID(), GetUsersPaginated(), GetUsersByIDs() (bulk)
│   │   └── content.go               # GetCandidateContent(), GetWatchHistoryBulk() (bulk, no N+1)
│   ├── model/
│   │   └── scorer.go                # Model Client: scoring algorithm, 30-50ms sim, failure sim
│   ├── cache/
│   │   └── redis.go                 # Cache Layer: Get(), Set(), Delete(), key builders
│   └── domain/
│       └── types.go                 # Shared structs: User, Content, WatchHistory, Recommendation
├── migrations/
│   ├── 001_init.up.sql              # CREATE TABLE + indexes (idempotent)
│   └── 001_init.down.sql            # DROP TABLE (reverse order for FK)
├── scripts/
│   └── seed.go                      # Deterministic data seeder (fixed seed = 42)
├── tests/
│   └── k6/
│       ├── load_test.js             # Single user: 100 RPS × 1 min
│       ├── batch_test.js            # Batch endpoint stress test
│       └── cache_test.js            # Cache hit-ratio effectiveness test
├── Dockerfile                       # Multi-stage build (builder → alpine runtime)
├── docker-compose.yml               # app + postgres:15 + redis:7
├── go.mod
├── go.sum
└── README.md                        # Setup, architecture, design decisions, perf results
```

---

## 2. System Architecture & Flow Diagrams

### 2.1 Layered Architecture

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
│              Error Formatting · Status Codes            │
└────────────────────────────┬────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────┐
│                    SERVICE LAYER                        │
│           internal/service/recommendation.go            │
│  Business Logic · Cache-or-Generate · Worker Pool Mgmt  │
│     Error Handling · Timeout Control · Bulk Prefetch    │
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

---

### 2.2 Single User Recommendation Flow

`GET /users/{user_id}/recommendations?limit=10`

```
CLIENT
  │
  │  GET /users/{user_id}/recommendations?limit=10
  ▼
┌─────────────────────────────────────────────────┐
│  ① HANDLER                                      │
│  Validate user_id (positive int, required)      │
│  Validate limit (1-50, default 10)              │
└────────────────────────┬────────────────────────┘
                         │ service.GetRecommendations(ctx, userID, limit)
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
│                 │   │    → recent timestamps          │
│                 │   │                                 │
│                 │   │  repo.GetCandidateContent()     │
│                 │   │    → WHERE id NOT IN (watched)  │
│                 │   │    → filter by subscription     │
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

---

### 2.3 Batch Recommendation Flow

`GET /recommendations/batch?page=1&limit=20`

```
CLIENT
  │
  │  GET /recommendations/batch?page=1&limit=20
  ▼
┌─────────────────────────────────────────────────┐
│  ① HANDLER                                      │
│  Validate page (>=1, default 1)                 │
│  Validate limit (1-100, default 20)             │
└────────────────────────┬────────────────────────┘
                         │ service.BatchRecommendations(ctx, page, limit)
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
│      WHERE user_id = ANY($1)                   │
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
                         │ collect via channels (success + failure)
                         ▼
┌─────────────────────────────────────────────────┐
│  ⑤ RESULT AGGREGATION                           │
│  success_count, failed_count                    │
│  measure processing_time_ms (time.Since(start)) │
└────────────────────────┬────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────┐
│  ⑥ HANDLER — Return 200 OK                      │
│  { page, limit, total_users,                    │
│    results[], summary, metadata }               │
└─────────────────────────────────────────────────┘
```

> **Partial Failure:** Individual worker errors must NOT halt the entire batch.
> Failed users return `{ user_id, status: "failed", error, message }` alongside successes.

---

## 3. Critical Implementation Notes

> ⚠️ **สิ่งเหล่านี้สำคัญมาก** — evaluator จะตรวจสอบโดยเฉพาะ

### 3.1 Avoid N+1 Queries (Batch Endpoint)

❌ **Wrong Pattern** — query ทีละ user ใน worker = N+1:

```go
// BAD: จะเกิด N queries สำหรับ N users
for _, userID := range userIDs {
    user, _    := repo.GetUser(ctx, userID)           // query #1..N
    history, _ := repo.GetWatchHistory(ctx, userID)   // query #1..N
    // ...
}
```

✅ **Correct Pattern** — bulk prefetch ก่อน แล้วค่อย fan-out:

```go
// GOOD: 2 queries รวม ไม่ว่าจะมีกี่ users
users, _     := repo.GetUsersByIDs(ctx, userIDs)        // 1 query
histories, _ := repo.GetWatchHistoryBulk(ctx, userIDs)  // 1 query

// แปลงเป็น map เพื่อ O(1) lookup ใน worker
userMap    := toUserMap(users)
historyMap := toHistoryMap(histories)

// workers ใช้ map ที่ prefetch ไว้แล้ว — ไม่มี DB call เพิ่ม
for _, userID := range userIDs {
    jobs <- Job{
        UserID:  userID,
        User:    userMap[userID],
        History: historyMap[userID],
    }
}
```

SQL สำหรับ bulk prefetch:

```sql
-- Bulk user fetch (1 query)
SELECT id, age, country, subscription_type, created_at
FROM users
WHERE id = ANY($1);   -- $1 = []int64 slice

-- Bulk watch history fetch (1 query)
SELECT uwh.user_id, uwh.content_id, c.genre, uwh.watched_at
FROM user_watch_history uwh
JOIN content c ON uwh.content_id = c.id
WHERE uwh.user_id = ANY($1)
ORDER BY uwh.user_id, uwh.watched_at DESC;
```

---

### 3.2 Connection Pool Management

```go
// internal/repository/db.go
config, _ := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
config.MaxConns          = 20
config.MinConns          = 5
config.MaxConnLifetime   = time.Hour
config.MaxConnIdleTime   = 30 * time.Minute
config.HealthCheckPeriod = time.Minute

pool, err := pgxpool.NewWithConfig(ctx, config)
```

> Rule of thumb: `MaxConns = (num_cores × 2) + num_disks` หรือ 20 สำหรับ dev

---

### 3.3 Timeout Handling

```go
const (
    DBTimeout    = 5 * time.Second
    RedisTimeout = 1 * time.Second
    BatchPerUser = 3 * time.Second  // per-user timeout in batch worker
)

// Service layer — ทุก operation ต้องมี context timeout
func (s *Service) GetRecommendations(ctx context.Context, userID int64, limit int) (*Response, error) {
    ctx, cancel := context.WithTimeout(ctx, DBTimeout)
    defer cancel()
    // ...
}

// Batch worker — timeout per user
func (w *worker) process(ctx context.Context, job Job) Result {
    ctx, cancel := context.WithTimeout(ctx, BatchPerUser)
    defer cancel()
    // ...
}
```

---

### 3.4 Cache Invalidation

```go
// เมื่อ user ดู content ใหม่ → ล้าง cache ของ user นั้นทุก limit variant
func (c *Cache) InvalidateUser(ctx context.Context, userID int64) error {
    pattern := fmt.Sprintf("rec:user:%d:limit:*", userID)
    keys, err := c.client.Keys(ctx, pattern).Result()
    if err != nil {
        return err
    }
    if len(keys) > 0 {
        return c.client.Del(ctx, keys...).Err()
    }
    return nil
}
```

> ⚠️ `KEYS` command อาจ block Redis บน keyspace ขนาดใหญ่ — ใช้ `SCAN` แทนใน production จริง

---

## 4. Database Schema Design

### 4.1 Users Table

```sql
CREATE TABLE users (
    id                BIGSERIAL PRIMARY KEY,
    age               INT NOT NULL CHECK (age > 0),
    country           VARCHAR(2) NOT NULL,          -- ISO 3166-1 alpha-2
    subscription_type VARCHAR(20) NOT NULL,          -- free | basic | premium
    created_at        TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_country      ON users(country);
CREATE INDEX idx_users_subscription ON users(subscription_type);
```

### 4.2 Content Table

```sql
CREATE TABLE content (
    id               BIGSERIAL PRIMARY KEY,
    title            VARCHAR(255) NOT NULL,
    genre            VARCHAR(50)  NOT NULL,          -- action|drama|comedy|thriller|documentary
    popularity_score DOUBLE PRECISION NOT NULL CHECK (popularity_score >= 0),
    created_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_content_genre      ON content(genre);
CREATE INDEX idx_content_popularity ON content(popularity_score DESC);
```

### 4.3 User Watch History Table

```sql
CREATE TABLE user_watch_history (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
    content_id BIGINT NOT NULL REFERENCES content(id) ON DELETE CASCADE,
    watched_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_watch_history_user      ON user_watch_history(user_id);
CREATE INDEX idx_watch_history_content   ON user_watch_history(content_id);
CREATE INDEX idx_watch_history_composite ON user_watch_history(user_id, watched_at DESC);
```

### 4.4 Index Strategy

| Index | Purpose | Query it Optimizes |
|---|---|---|
| `idx_watch_history_user` | Fast lookup ของ content ที่ user ดูแล้ว | `WHERE user_id = $1` |
| `idx_watch_history_composite` | Watch history ล่าสุดต่อ user | `WHERE user_id = $1 ORDER BY watched_at DESC` |
| `idx_content_genre` | Filter ตาม genre ตอน fetch candidates | `WHERE genre = $1` |
| `idx_content_popularity` | เรียงตาม popularity ก่อน score | `ORDER BY popularity_score DESC` |
| `idx_users_country` | Geo-based filtering | `WHERE country = $1` |
| `idx_users_subscription` | Filter ตาม subscription tier | `WHERE subscription_type = $1` |

---

## 5. Recommendation Model & Scoring Algorithm

### 5.1 Data Inputs (from DB via Repository)

**User Context** (from `users` table):
- `user_id` — for cache key generation
- `age` — can influence content appropriateness
- `country` — for geo-specific content preferences
- `subscription_type` — affects content access levels

**Watch History** (from `user_watch_history JOIN content`):

```sql
SELECT c.id, c.genre, uwh.watched_at
FROM user_watch_history uwh
JOIN content c ON uwh.content_id = c.id
WHERE uwh.user_id = $1
ORDER BY uwh.watched_at DESC
LIMIT 50;
```

**Candidate Content** (from `content`, excluding watched):

```sql
SELECT id, title, genre, popularity_score, created_at
FROM content
WHERE id NOT IN (
    SELECT content_id FROM user_watch_history WHERE user_id = $1
)
ORDER BY popularity_score DESC
LIMIT 100;
```

---

### 5.2 Scoring Algorithm (3 Steps)

#### Step 1 — Genre Preference Weights

```go
// Build genre count map from watch history
genreCounts  := map[string]int{}
totalWatches := 0
for _, item := range watchHistory {
    genreCounts[item.Genre]++
    totalWatches++
}

// Normalize to preference scores (0.0 - 1.0)
genrePrefs := map[string]float64{}
for genre, count := range genreCounts {
    genrePrefs[genre] = float64(count) / float64(totalWatches)
}
// Example result: {"action": 0.556, "drama": 0.278, "comedy": 0.167}
```

#### Step 2 — Recency Factor

```go
// Time decay: newer content gets slight boost
daysSinceCreation := time.Since(content.CreatedAt).Hours() / 24
recencyFactor     := 1.0 / (1.0 + daysSinceCreation/365.0)

// Examples:
//   Content from 1 week ago  → recencyFactor ~= 0.98
//   Content from 1 year ago  → recencyFactor ~= 0.50
```

#### Step 3 — Final Score Computation

```go
for _, candidate := range candidates {
    popularityComponent := candidate.PopularityScore * 0.40

    genreBoost := 0.10  // default for unseen genres
    if pref, ok := genrePrefs[candidate.Genre]; ok {
        genreBoost = pref
    }
    genreComponent := genreBoost * 0.35

    recencyComponent := recencyFactor(candidate.CreatedAt) * 0.15

    // Exploration: controlled randomness [-0.005, +0.005]
    randomNoise := (rng.Float64()*0.10 - 0.05) * 0.10

    candidate.Score = popularityComponent + genreComponent + recencyComponent + randomNoise
}

// Sort DESC by score → take top N
sort.Slice(candidates, func(i, j int) bool {
    return candidates[i].Score > candidates[j].Score
})
return candidates[:limit]
```

### 5.3 Score Weight Rationale

| Component | Weight | Rationale |
|---|---|---|
| Popularity | **40%** | Strongest universal signal — popular content is broadly enjoyed |
| Genre Preference | **35%** | Core personalization — match user's historical genre taste |
| Recency | **15%** | Slight boost for newer content to keep recommendations fresh |
| Exploration Noise | **10%** | Controlled randomness to avoid filter bubbles & add diversity |

### 5.4 Simulation Requirements

```go
// 30-50ms latency simulation per request (ตาม spec)
time.Sleep(time.Duration(30+rng.Intn(21)) * time.Millisecond)

// 1.5% random failure simulation (spec บอก 1-2%)
if rng.Float64() < 0.015 {
    return nil, ErrModelUnavailable  // → HTTP 503
}
```

---

## 6. Content Filtering Rules

### 6.1 Mandatory: Watch History Exclusion

ห้าม recommend content ที่ user ดูแล้วเด็ดขาด:

```sql
WHERE id NOT IN (
    SELECT content_id FROM user_watch_history WHERE user_id = $1
)
```

### 6.2 Optional: Subscription Tier Filter

```go
// กรอง candidates ตาม subscription type ก่อนส่งเข้า model
func filterBySubscription(candidates []Content, subType string) []Content {
    // ตัวอย่าง logic:
    //   free    → ล็อค content ที่ popularity_score > 0.9 (premium-tier content)
    //   basic   → content ทั้งหมดเข้าถึงได้
    //   premium → content ทั้งหมด + exclusive content
    filtered := []Content{}
    for _, c := range candidates {
        if subType == "free" && c.PopularityScore > 0.9 {
            continue
        }
        filtered = append(filtered, c)
    }
    return filtered
}
```

### 6.3 Optional: Geographic Availability Filter

```go
// กรองตาม country ถ้ามี geo-restriction
func filterByCountry(candidates []Content, country string) []Content {
    // ใน seed data ไม่มี country field บน content
    // → implement เป็น pass-through stub ก็ได้
    // → แต่ต้อง document intent ใน README Design Decisions
    return candidates
}
```

---

## 7. API Endpoint Specifications

### 7.1 `GET /users/{user_id}/recommendations`

**Path Parameters:**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `user_id` | integer | Yes | Unique user identifier (positive int) |

**Query Parameters:**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `limit` | integer | No | Recs to return. Default: 10, Min: 1, Max: 50 |

**Success Response (200 OK):**

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
    "total_count": 10
  }
}
```

**Error Responses:**

| Status | Trigger | Error Body |
|---|---|---|
| 404 | user_id ไม่มีในระบบ | `{"error":"user_not_found","message":"User with ID {user_id} does not exist"}` |
| 400 | limit ผิด format หรือเกิน range | `{"error":"invalid_parameter","message":"Invalid limit parameter"}` |
| 500 | DB error หรือ unexpected | `{"error":"internal_error","message":"An unexpected error occurred"}` |
| 503 | model simulation failure (1.5%) | `{"error":"model_unavailable","message":"Recommendation model is temporarily unavailable"}` |

---

### 7.2 `GET /recommendations/batch`

**Query Parameters:**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `page` | integer | No | Page number. Default: 1, Min: 1 |
| `limit` | integer | No | Users per page. Default: 20, Min: 1, Max: 100 |

**Success Response (200 OK):**

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
      "error": "model_inference_timeout",
      "message": "Recommendation generation exceeded timeout limit"
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

**Behavioral Requirements:**

| Requirement | Implementation |
|---|---|
| Pagination | `LIMIT $1 OFFSET (page-1)*limit` |
| Concurrency Control | Bounded goroutine pool (10 workers) |
| Partial Failure | Each user result is independent — worker error → failed entry, batch continues |
| N+1 Prevention | Bulk prefetch user data + watch history before spawning workers |

---

## 8. Caching Strategy

### 8.1 Cache Key Pattern

```
rec:user:{user_id}:limit:{limit}

Examples:
  rec:user:1:limit:10
  rec:user:42:limit:20
  rec:user:42:limit:50   <-- different limit = different cache entry
```

### 8.2 TTL Strategy

| Cache Type | TTL | Rationale |
|---|---|---|
| Single-user recommendations | **10 minutes** | Balances freshness vs. DB load |
| Batch aggregate results | **Not cached** | Per-user caches still apply inside batch |

### 8.3 Cache Flow

```
Request comes in
       │
       ▼
  cache.Get(key)
       │
  ┌────┴────┐
  │         │
 HIT       MISS
  │         │
  ▼         ▼
Return    Generate fresh recommendations
JSON  +   → cache.Set(key, data, TTL=10min)
cache_    → return + cache_hit=false
hit=true

  If cache.Get() returns error:
  → log warning (non-fatal)
  → fall through to generate fresh
  → do NOT return error to client
```

### 8.4 Cache Invalidation

```
Trigger: user watches new content  (POST /users/{id}/watch)
Action:  DEL all keys matching  rec:user:{id}:limit:*

Go implementation:
  pattern := fmt.Sprintf("rec:user:%d:limit:*", userID)
  keys, _ := redis.Keys(ctx, pattern).Result()
  redis.Del(ctx, keys...)

Production note: ใช้ SCAN แทน KEYS ถ้า keyspace ใหญ่
```

### 8.5 Cache Metadata in Response

ทุก response ต้องมี field นี้เสมอ (ใช้ debug และ monitor ได้):

```json
"metadata": {
  "cache_hit": true,
  "generated_at": "2026-03-14T10:30:00Z",
  "total_count": 10
}
```

---

## 9. Data Seeding

### 9.1 Minimum Dataset

| Entity | Min Count | **Actual Seeded** | Notes |
|---|---|---|---|
| Users | 20 | **300** | Ages 18-65, 5+ countries, 3 subscription tiers |
| Content Items | 50 | **50** | 5+ genres, power-law popularity distribution |
| Watch History Records | 200 | **3,000** | ~10 watches per user on average |
| Distinct Genres | 5 | **5** | action, drama, comedy, thriller, documentary |

> **Why 300 users?** batch_test.js tests pages 1–3 with limits 20/50/100.
> The most demanding combo is page=3, limit=100 → offset=200 → needs 201+ users.
> 300 covers all 9 page×limit combinations with data.

### 9.2 Distribution Requirements

```go
// Fixed seed for determinism — ใช้ pattern นี้เสมอ
rng := rand.New(rand.NewSource(42))

// Subscription weights: free 50%, basic 30%, premium 20%
func weightedChoice(rng *rand.Rand, choices []string, weights []float64) string {
    r := rng.Float64()
    cumulative := 0.0
    for i, w := range weights {
        cumulative += w
        if r < cumulative {
            return choices[i]
        }
    }
    return choices[len(choices)-1]
}

subscriptions := []string{"free", "basic", "premium"}
weights       := []float64{0.5, 0.3, 0.2}

// Countries (ISO 3166-1 alpha-2)
countries := []string{"US", "GB", "CA", "AU", "DE", "TH", "JP"}

// Power-law popularity: math.Pow(x, 2.0) skews toward 0
// → few items get high popularity (realistic long-tail)
popularityScore := math.Pow(rng.Float64(), 2.0)  // 0.0 – 1.0

// Watch history: bias toward popular content
func popularityBiasedPick(rng *rand.Rand, contents []Content) Content {
    totalWeight := 0.0
    for _, c := range contents {
        totalWeight += c.PopularityScore
    }
    r := rng.Float64() * totalWeight
    cumulative := 0.0
    for _, c := range contents {
        cumulative += c.PopularityScore
        if r <= cumulative {
            return c
        }
    }
    return contents[len(contents)-1]
}
```

### 9.3 Seed Script Structure

```go
// scripts/seed.go
func main() {
    ctx := context.Background()
    db  := connectDB()
    rng := rand.New(rand.NewSource(42))  // fixed seed

    // Step 1: TRUNCATE — idempotent, safe to run multiple times
    db.Exec(ctx, `TRUNCATE users, content, user_watch_history RESTART IDENTITY CASCADE`)

    // Step 2: Seed content (50 items, power-law popularity)
    contentIDs := seedContent(ctx, db, rng, 50)

    // Step 3: Seed users (300 users — covers all batch page/limit combos)
    userIDs := seedUsers(ctx, db, rng, 300)

    // Step 4: Seed watch history (3000 records, popularity-biased)
    seedWatchHistory(ctx, db, rng, userIDs, contentIDs, 3000)

    fmt.Println("Seed complete: 300 users, 50 content, 3000 watch history records")
}
```

---

## 10. Docker Configuration

### 10.1 docker-compose.yml

```yaml
version: '3.8'

services:
  app:
    build: .
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgresql://user:password@postgres:5432/recommendations?sslmode=disable
      - REDIS_URL=redis://redis:6379
      - SERVER_PORT=8080
      - CACHE_TTL_MINUTES=10
      - WORKER_POOL_SIZE=10
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_started
    restart: unless-stopped

  postgres:
    image: postgres:15
    environment:
      - POSTGRES_USER=user
      - POSTGRES_PASSWORD=password
      - POSTGRES_DB=recommendations
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U user -d recommendations"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7
    ports:
      - "6379:6379"
    restart: unless-stopped

volumes:
  postgres_data:
```

### 10.2 Dockerfile (Multi-Stage Build)

```dockerfile
# Stage 1: Build
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/server

# Stage 2: Runtime (minimal image)
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /root/
COPY --from=builder /app/server .
COPY --from=builder /app/migrations ./migrations
EXPOSE 8080
CMD ["./server"]
```

### 10.3 One-Command Startup

```bash
docker compose up --build
```

> App startup sequence: `main.go` → run migrations → (seed if DB empty) → start HTTP :8080

### 10.4 Startup Logic ใน main.go (Seed-on-Empty Pattern)

```go
func main() {
    db := connectDB()

    // Step 1: migrations — idempotent, รันกี่รอบก็ปลอดภัย
    runMigrations(db)

    // Step 2: seed เฉพาะตอน DB ว่างเปล่าเท่านั้น
    // → restart service / แก้โค้ด / build ใหม่ = ข้อมูลไม่หาย
    var count int
    db.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
    if count == 0 {
        log.Println("Empty DB detected → running seed...")
        runSeed(db)  // TRUNCATE → insert fresh data
    } else {
        log.Printf("DB already has %d users → skipping seed", count)
    }

    // Step 3: start HTTP server
    startServer()
}
```

**Behavior ที่ได้:**

| Action | Migrations | Seed | Data |
|---|---|---|---|
| `docker compose up --build` (ครั้งแรก) | ✅ run | ✅ run (DB ว่าง) | สร้างใหม่ |
| `docker compose up` (ครั้งต่อๆ ไป) | ✅ run (IF NOT EXISTS) | ⏭️ skip | คงเดิม |
| แก้โค้ด → `docker compose up --build` | ✅ run | ⏭️ skip | คงเดิม |
| อยากล้างแล้ว seed ใหม่ | — | `go run ./scripts/seed.go` | reset |
| อยากล้างทุกอย่าง | — | `docker compose down -v` | ลบทั้งหมด |

---

## 11. Performance Testing (k6)

### 11.1 load_test.js — Single User Load Test

```javascript
// tests/k6/load_test.js
import http from 'k6/http';
import { check, sleep } from 'k6';

export let options = {
  stages: [
    { duration: '30s', target: 50  },   // ramp up to 50 VUs
    { duration: '1m',  target: 100 },   // hold at 100 VUs (~100 RPS with sleep 0.1)
    { duration: '30s', target: 0   },   // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
    http_req_failed:   ['rate<0.03'],
  },
};

export default function () {
  const userId = Math.floor(Math.random() * 300) + 1;  // covers full 300-user dataset
  const res = http.get(
    `http://localhost:8080/users/${userId}/recommendations?limit=10`,
    { timeout: '5s' }
  );
  check(res, {
    'status is 200':       (r) => r.status === 200,
    'has recommendations': (r) => JSON.parse(r.body).recommendations.length > 0,
    'has metadata':        (r) => JSON.parse(r.body).metadata !== undefined,
    'cache_hit field set': (r) => JSON.parse(r.body).metadata.cache_hit !== undefined,
  });
  sleep(0.1);
}
```

### 11.2 batch_test.js — Batch Stress Test

```javascript
// tests/k6/batch_test.js
import http from 'k6/http';
import { check, sleep } from 'k6';

export let options = {
  stages: [
    { duration: '30s', target: 10 },
    { duration: '1m',  target: 20 },
    { duration: '30s', target: 0  },
  ],
  thresholds: {
    http_req_duration: ['p(95)<3000'],   // batch takes longer due to model sim
    http_req_failed:   ['rate<0.05'],
  },
};

const pageSizes = [20, 50, 100];

export default function () {
  const page  = Math.floor(Math.random() * 3) + 1;
  const limit = pageSizes[Math.floor(Math.random() * pageSizes.length)];

  const res = http.get(
    `http://localhost:8080/recommendations/batch?page=${page}&limit=${limit}`,
    { timeout: '30s' }
  );
  check(res, {
    'status is 200':       (r) => r.status === 200,
    'has results':         (r) => JSON.parse(r.body).results.length > 0,
    'has summary':         (r) => JSON.parse(r.body).summary !== undefined,
    'has success_count':   (r) => JSON.parse(r.body).summary.success_count >= 0,
    'has processing_time': (r) => JSON.parse(r.body).summary.processing_time_ms > 0,
  });
  sleep(1);
}
```

### 11.3 cache_test.js — Cache Effectiveness Test

```javascript
// tests/k6/cache_test.js
import http from 'k6/http';
import { check } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const cacheHitRate   = new Rate('cache_hit_rate');
const cacheLatency   = new Trend('cache_hit_latency_ms');
const noCacheLatency = new Trend('cache_miss_latency_ms');

export let options = {
  vus: 10,
  duration: '2m',   // 2 min: first pass warms cache, second pass measures hits
  thresholds: {
    cache_hit_rate: ['rate>0.70'],   // expect >70% hits after warm-up
  },
};

export default function () {
  // Small fixed user set → high cache hit probability after warm-up
  const userId = Math.floor(Math.random() * 5) + 1;
  const res = http.get(
    `http://localhost:8080/users/${userId}/recommendations?limit=10`
  );

  if (res.status === 200) {
    const body  = JSON.parse(res.body);
    const isHit = body.metadata.cache_hit === true;
    cacheHitRate.add(isHit);
    if (isHit) {
      cacheLatency.add(res.timings.duration);
    } else {
      noCacheLatency.add(res.timings.duration);
    }
  }
  check(res, { 'status is 200': (r) => r.status === 200 });
}
```

### 11.4 Required Metrics & Thresholds

| Metric | Target | k6 Threshold |
|---|---|---|
| Average Latency | < 200ms | `http_req_duration avg<200` |
| P95 Latency | < 500ms | `http_req_duration p(95)<500` |
| P99 Latency | < 1000ms | `http_req_duration p(99)<1000` |
| Throughput | > 80 req/s sustained | Monitor `http_reqs` counter |
| Error Rate | < 3% | `http_req_failed rate<0.03` |
| Cache Hit Rate | > 70% after warm-up | Custom `cache_hit_rate` metric |

### 11.5 Actual k6 Results (after tuning)

#### Single User Load Test

| Metric | Target | **Result** | Status |
|---|---|---|---|
| Avg Latency | <200ms | **1.30ms** | ✅ |
| P95 Latency | <500ms | **2.08ms** | ✅ |
| P99 Latency | <1000ms | **2.71ms** | ✅ |
| Throughput | >80 req/s | **550.8 req/s** | ✅ |
| Error Rate | <3% | **0.01%** | ✅ |

#### Batch Endpoint Stress Test

| Metric | Target | **Result** | Status |
|---|---|---|---|
| Avg Latency | — | **4.96ms** | ✅ |
| P95 Latency | <3000ms | **7.66ms** | ✅ |
| P99 Latency | — | **~10ms** | ✅ |
| Throughput | — | **10.9 req/s** | ✅ |
| Error Rate | <5% | **0.00%** | ✅ |
| All Checks | 100% pass | **100.00%** | ✅ |

#### Cache Effectiveness Test

| Metric | Target | **Result** | Status |
|---|---|---|---|
| Cache Hit Rate | >70% | **100.00%** | ✅ |
| Avg Latency | — | **1.28ms** | ✅ |
| P95 Latency | — | **2.11ms** | ✅ |
| Throughput | — | **6,780 req/s** | ✅ |
| Error Rate | — | **0.00%** | ✅ |

---

## 12. Go Dependencies

```go
// go.mod
module github.com/yourname/recommendation-service

go 1.26

require (
    github.com/jackc/pgx/v5              v5.5.4   // PostgreSQL driver + pgxpool
    github.com/redis/go-redis/v9         v9.4.0   // Redis client
    github.com/gin-gonic/gin             v1.9.1   // HTTP router & middleware
    github.com/golang-migrate/migrate/v4 v4.17.0  // DB migration runner
    github.com/stretchr/testify          v1.9.0   // Test assertions
)
```

> **Alternative:** Go 1.26 มี `net/http` ServeMux รองรับ `GET /users/{user_id}/recommendations`
> ได้แล้วโดยไม่ต้องใช้ Gin — ลด external dependencies ได้ถ้าต้องการ

---

## 13. Windows 11 Setup Guide

### 13.1 Prerequisites

| Tool | Version | Install |
|---|---|---|
| **Go** | 1.26.1 | https://go.dev/dl/ → `go1.26.1.windows-amd64.msi` |
| **Docker Desktop** | Latest | https://www.docker.com/products/docker-desktop (WSL2 backend) |
| **Git** | Latest | https://git-scm.com/download/win |
| **k6** | Latest | `winget install k6` หรือ https://k6.io/docs/get-started/installation/ |
| **VS Code** | Latest (optional) | https://code.visualstudio.com + Go extension (`golang.go`) |

### 13.2 Verify Installations (PowerShell)

```powershell
go version             # go version go1.26.1 windows/amd64
docker --version       # Docker version 24.x.x
docker compose version # Docker Compose version v2.x.x
k6 version             # k6 v0.x.x
git --version          # git version 2.x.x
```

### 13.3 Project Bootstrap Commands

```powershell
# 1. สร้าง project directory
mkdir recommendation-service
cd recommendation-service

# 2. Initialize Go module
go mod init github.com/yourname/recommendation-service

# 3. Install dependencies
go get github.com/jackc/pgx/v5
go get github.com/redis/go-redis/v9
go get github.com/gin-gonic/gin
go get "github.com/golang-migrate/migrate/v4"
go get "github.com/golang-migrate/migrate/v4/database/postgres"
go get "github.com/golang-migrate/migrate/v4/source/file"

# 4. Build & start all services
docker compose up --build

# 5. Run migrations (ถ้า app ไม่ auto-run)
go run ./cmd/migrate/main.go up

# 6. Seed database
go run ./scripts/seed.go

# 7. Test API
curl http://localhost:8080/users/1/recommendations?limit=10
curl "http://localhost:8080/recommendations/batch?page=1&limit=20"

# 8. Run k6 tests
k6 run tests/k6/load_test.js
k6 run tests/k6/batch_test.js
k6 run tests/k6/cache_test.js
```

### 13.4 Environment Variables

```env
DATABASE_URL=postgresql://user:password@localhost:5432/recommendations?sslmode=disable
REDIS_URL=redis://localhost:6379
SERVER_PORT=8080
CACHE_TTL_MINUTES=10
WORKER_POOL_SIZE=10
DB_MAX_CONNS=20
DB_MIN_CONNS=5
```

---

## 14. Migration & Seed File Patterns

### 14.1 Migration Files

**`migrations/001_init.up.sql`**

```sql
-- Idempotent: ใช้ IF NOT EXISTS ทุก statement
CREATE TABLE IF NOT EXISTS users (
    id                BIGSERIAL PRIMARY KEY,
    age               INT NOT NULL CHECK (age > 0),
    country           VARCHAR(2) NOT NULL,
    subscription_type VARCHAR(20) NOT NULL,
    created_at        TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS content (
    id               BIGSERIAL PRIMARY KEY,
    title            VARCHAR(255) NOT NULL,
    genre            VARCHAR(50) NOT NULL,
    popularity_score DOUBLE PRECISION NOT NULL CHECK (popularity_score >= 0),
    created_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_watch_history (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
    content_id BIGINT NOT NULL REFERENCES content(id) ON DELETE CASCADE,
    watched_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_users_country           ON users(country);
CREATE INDEX IF NOT EXISTS idx_users_subscription      ON users(subscription_type);
CREATE INDEX IF NOT EXISTS idx_content_genre           ON content(genre);
CREATE INDEX IF NOT EXISTS idx_content_popularity      ON content(popularity_score DESC);
CREATE INDEX IF NOT EXISTS idx_watch_history_user      ON user_watch_history(user_id);
CREATE INDEX IF NOT EXISTS idx_watch_history_content   ON user_watch_history(content_id);
CREATE INDEX IF NOT EXISTS idx_watch_history_composite ON user_watch_history(user_id, watched_at DESC);
```

**`migrations/001_init.down.sql`**

```sql
-- Drop ในลำดับย้อนกลับ (FK constraint)
DROP TABLE IF EXISTS user_watch_history;
DROP TABLE IF EXISTS content;
DROP TABLE IF EXISTS users;
```

### 14.2 Key Notes

- `golang-migrate` expects: `{version}_{description}.up.sql` / `{version}_{description}.down.sql`
- Run migrations ใน `main.go` ก่อน start HTTP server เสมอ
- Seed script ต้อง `TRUNCATE ... RESTART IDENTITY CASCADE` ก่อน insert (idempotent)
- ใช้ `math/rand` ไม่ใช่ `crypto/rand` — ต้องการ deterministic ไม่ต้องการ secure random

---

## 15. Git Commit Strategy

> PDF ระบุชัดว่า: *"Include meaningful commit messages demonstrating your development process"*
> Reviewer จะดู commit history เพื่อประเมินว่าคิดเป็น step ไม่ใช่ dump code ทีเดียว

### 15.1 Setup (ทำครั้งเดียวตอนเริ่ม)

```bash
cd recommendation-service
git init
git branch -M main
```

สร้าง `.gitignore` ก่อน commit แรกเลย:

```gitignore
# Binaries
/server
*.exe

# Environment
.env
*.env

# Go
vendor/

# IDE
.vscode/
.idea/

# OS
.DS_Store
Thumbs.db

# Test output
k6-results/
*.log
```

```bash
# Commit แรก
git add .gitignore go.mod go.sum
git commit -m "chore: initial project setup with Go module and gitignore"
```

---

### 15.2 Commit Flow ทีละ Layer

ทำ layer เสร็จ → commit ทันที → ทำ layer ต่อไป

**Actual commit history (จาก `git log --oneline`):**

```
d1415ae chore: initial project setup with Go module and gitignore
        │
        ▼
2e5f090 feat: add domain types for user, content, recommendation and batch
        │
        ▼
fd9ae56 feat: add model scoring algorithm with genre preference and recency factor
        │
        ▼
0c9a84f feat: add repository layer with bulk prefetch to avoid N+1
        │
        ▼
d3efdfa feat: add deterministic seed script with power-law distribution
        │
        ▼
f8e2eac feat: add service layer with cache-or-generate and worker pool
        │
        ▼
9363c0e feat: add HTTP handler layer with input validation and error handling
        │
        ▼
e9a12a7 feat: add Redis cache layer with TTL and key invalidation
        │
        ▼
17cd8e2 feat: wire all dependencies in main with seed-on-empty startup
        │
        ▼
9731b66 feat: add database migrations with indexes
        │
        ▼
3585e62 feat: add multi-stage Dockerfile and docker-compose with health checks
        │
        ▼
c90d52a feat: add k6 load, batch, and cache effectiveness test scripts
        │
        ▼
a41f0b1 chore: add golang-migrate dependency for migration management
        │
        ▼
f142bf9 docs: add README with architecture, design decisions, and perf results
        │
        ▼
f9ac0ab chore: improve Makefile with Docker one-command startup and k6 targets
        │
        ▼
e96a980 perf(seed): increase dataset to 300 users for valid batch pagination
```

---

### 15.3 Commands ที่ใช้จริงแต่ละ Layer

**Layer 1 — Domain Types:**
```bash
git add internal/domain/types.go
git commit -m "feat: add domain types for user, content, recommendation and batch"
```

**Layer 2 — Cache:**
```bash
git add internal/cache/redis.go
git commit -m "feat: add Redis cache layer with TTL and key invalidation"
```

**Layer 3 — Repository:**
```bash
git add internal/repository/
git commit -m "feat: add repository layer with bulk prefetch to avoid N+1"
```

**Layer 4 — Model:**
```bash
git add internal/model/scorer.go
git commit -m "feat: add model scoring algorithm with genre preference and recency factor"
```

**Layer 5 — Service:**
```bash
git add internal/service/recommendation.go
git commit -m "feat: add service layer with cache-or-generate and worker pool"
```

**Layer 6 — Handler:**
```bash
git add internal/handler/handler.go
git commit -m "feat: add HTTP handler layer with input validation and error handling"
```

**Layer 7 — Main (Wire everything):**
```bash
git add cmd/server/main.go
git commit -m "feat: wire all dependencies in main with seed-on-empty startup"
```

**Layer 8 — Migrations + Seed:**
```bash
git add migrations/ scripts/
git commit -m "feat: add database migrations with indexes and deterministic seed script"
```

**Layer 9 — Docker:**
```bash
git add Dockerfile docker-compose.yml
git commit -m "feat: add multi-stage Dockerfile and docker-compose with health checks"
```

**Layer 10 — k6 Tests:**
```bash
git add tests/
git commit -m "feat: add k6 load, batch, and cache effectiveness test scripts"
```

**หลัง run k6 แล้วเจอ bug / tune:**
```bash
git add internal/service/recommendation.go
git commit -m "fix: handle partial failure and timeout correctly in batch worker"

git add internal/service/recommendation.go internal/repository/
git commit -m "perf: tune worker pool to 10 and DB max connections to 20 after k6 results"
```

**สุดท้าย:**
```bash
git add README.md
git commit -m "docs: add README with architecture, design decisions, and k6 performance results"
```

---

### 15.4 Push ตอนสุดท้าย (ก่อนส่ง)

> **✅ DONE — Push เสร็จแล้ว**
>
> **สถานะ:**
> - [x] ได้รับ GitHub repo URL แล้ว: `https://github.com/CN164/recommendation-service.git`
> - [x] `docker compose up --build` รันผ่านแล้วไม่มี error
> - [x] `curl http://localhost:8080/users/1/recommendations` ตอบกลับถูกต้อง
> - [x] รัน k6 ทั้ง 3 scripts แล้ว มีตัวเลขกรอก README ได้
> - [x] README.md เขียนครบทั้ง 5 sections แล้ว
> - [x] Push ขึ้น GitHub สำเร็จ

```bash
# Actual commands that were run:
git remote add origin https://github.com/CN164/recommendation-service.git
git push -u origin main
```

---

### 15.5 Commit Message Convention

| Prefix | ใช้เมื่อ | ตัวอย่าง |
|---|---|---|
| `chore:` | setup, config ทั่วไป | `chore: initial project setup` |
| `feat:` | เพิ่ม feature ใหม่ | `feat: add Redis cache layer` |
| `fix:` | แก้ bug | `fix: handle nil user in batch worker` |
| `perf:` | tune performance | `perf: tune worker pool size after k6` |
| `docs:` | README, comments | `docs: add design decisions to README` |
| `test:` | เพิ่ม/แก้ tests | `test: add k6 cache effectiveness script` |

---


## 16. README.md Draft Template

> กรอกหลัง implement เสร็จ — section นี้คือ template พร้อม placeholder ครบ 5 หัวข้อตาม spec

---

### 14.1 Setup Instructions

**Prerequisites:**
- Go 1.26+
- Docker Desktop (WSL2 enabled on Windows 11)
- k6 (for performance testing)

**Quick Start (one command):**
```bash
docker compose up --build
```

**Manual Setup:**
```bash
# 1. Run migrations
go run ./cmd/migrate/main.go up

# 2. Seed database
go run ./scripts/seed.go

# 3. Start server
go run ./cmd/server/main.go
```

**Verify it works:**
```bash
curl http://localhost:8080/users/1/recommendations?limit=10
```

---

### 14.2 Architecture Overview

**System Layers:**

| Layer | Package | Responsibility |
|---|---|---|
| Handler | `internal/handler` | HTTP routing, input validation, JSON response formatting |
| Service | `internal/service` | Business logic, cache orchestration, worker pool management |
| Repository | `internal/repository` | SQL queries, bulk prefetch, DB connection pool |
| Model Client | `internal/model` | Heuristic scoring algorithm, latency/failure simulation |
| Cache | `internal/cache` | Redis Get/Set/Delete, key building, TTL management |
| Domain | `internal/domain` | Shared Go structs (User, Content, WatchHistory, Recommendation) |

**Data Flow — Single Request:**
```
Request → Handler (validate) → Service (check cache)
  → [Cache Hit]  return JSON immediately
  → [Cache Miss] Repository (fetch user + history + candidates)
               → Model (score candidates, 30-50ms sim)
               → Cache (store result, TTL 10min)
               → return JSON
```

**Data Flow — Batch Request:**
```
Request → Handler → Service
  → Repository: bulk prefetch ALL users + histories in 2 queries
  → Worker Pool (10 goroutines, fan-out)
    → each worker: check cache → score (cache miss only) → result
  → Aggregate: success_count, failed_count, processing_time_ms
  → return JSON
```

**How the Recommendation Model integrates with DB queries:**
The Model Client receives pre-fetched data from the Repository layer:
1. `genrePrefs` computed from `user_watch_history JOIN content`
2. `candidates` from `content WHERE id NOT IN (watched)` 
3. Model applies the scoring formula using these DB-sourced inputs — not random data

---

### 14.3 Design Decisions

**Caching Strategy & TTL Rationale:**
- Key includes `limit` because `?limit=10` and `?limit=20` are different result sets
- TTL of 10 minutes: watch history changes rarely within a session; 10 min reduces DB load significantly while keeping recommendations reasonably fresh
- Cache failure is non-fatal: Redis down → fallback to DB generation, log warning only
- Invalidation on new watch event removes all limit variants for that user

**Concurrency Control Approach:**
- Worker pool capped at 10 goroutines to avoid overwhelming DB connection pool (MaxConns=20)
- Bounded channels (`make(chan, N)`) prevent goroutine leaks
- Bulk prefetch (2 queries total) before fan-out eliminates N+1 problem

**Error Handling Philosophy:**
- 404: business-level not-found (user doesn't exist)
- 400: client validation error (bad parameters)
- 503: model simulation failure — retryable, 1.5% rate
- 500: unexpected internal error — logged with context
- Partial batch failure: one user failing never stops other users from completing

**Database Indexing Strategy:**
- Composite index `(user_id, watched_at DESC)` avoids sort for recent history queries
- `popularity_score DESC` index enables efficient top-N candidate retrieval
- `user_id = ANY($1)` uses index scan for bulk prefetch queries

**Scoring Algorithm Weight Choices:**
- 40% popularity: Works for cold-start and new users with limited history
- 35% genre preference: Strongest personalization signal from actual behavior
- 15% recency: Prevents old content from dominating equally-scored items
- 10% exploration noise: Ensures diversity across repeated requests

---

### 14.4 Performance Results

> *(Fill in after running k6 tests)*

**Test Environment:**
- Machine: [CPU / RAM]
- Docker resources: [CPU cores / RAM allocated]
- Dataset: 20 users, 50 content items, 200+ watch history records

**Single User Load Test (load_test.js):**

| Metric | Result | Threshold | Status |
|---|---|---|---|
| Avg Latency | ___ ms | < 200ms | ✅ / ❌ |
| P95 Latency | ___ ms | < 500ms | ✅ / ❌ |
| P99 Latency | ___ ms | < 1000ms | ✅ / ❌ |
| Throughput | ___ req/s | > 80 req/s | ✅ / ❌ |
| Error Rate | __% | < 3% | ✅ / ❌ |

**Batch Endpoint Test (batch_test.js):**

| Metric | Result | Threshold | Status |
|---|---|---|---|
| P95 Latency | ___ ms | < 3000ms | ✅ / ❌ |
| Error Rate | __% | < 5% | ✅ / ❌ |

**Cache Effectiveness Test (cache_test.js):**

| Metric | Result | Threshold | Status |
|---|---|---|---|
| Cache Hit Rate | __% | > 70% | ✅ / ❌ |
| Avg Latency (cache hit) | ___ ms | — | — |
| Avg Latency (cache miss) | ___ ms | — | — |

**Bottleneck Analysis:**
- Model simulation sleep (30-50ms) dominates response time on cache miss
- After warm-up (~30s), cache hit rate stabilizes at X%
- DB connection pool saturation observed at approximately X concurrent users

---

### 14.5 Trade-offs and Future Improvements

**Known Limitations:**
- Small seed dataset (20 users, 50 content) — performance numbers may not reflect production scale
- `KEYS` command for cache invalidation blocks Redis on large keyspaces — use `SCAN` in production
- No persistent Redis — cold start after Redis restart drops all cache entries
- Model failure is pure random — production systems would use circuit breakers with state

**Scalability Considerations:**
- App service is stateless — horizontal scaling behind a load balancer requires no changes
- Redis Cluster or Redis Sentinel needed for cache HA at scale
- PostgreSQL read replicas recommended for heavy recommendation query load
- OFFSET pagination degrades at large page numbers — cursor-based pagination preferred at scale

**Proposed Enhancements (if time permitted):**
1. **Circuit breaker** for model client using `github.com/sony/gobreaker`
2. **Cache warming** — pre-populate top-N active user caches on startup
3. **Prometheus metrics** endpoint — cache hit rate, latency histograms, worker pool utilization
4. **Rate limiting** per user_id to prevent recommendation endpoint abuse
5. **Cursor-based pagination** replacing OFFSET for large user tables
6. **Real geo/subscription filtering** with proper content tier tables in DB schema
7. **Unit + integration tests** with `testify` and `pgxmock` for repository layer

---

## 17. Deliverables Checklist

```
□  Source Code
   ├── cmd/server/main.go
   │     (entry point + dependency injection: DB pool, Redis, repo, service, handler)
   ├── internal/handler/handler.go
   │     (HTTP layer only — no business logic, no SQL)
   ├── internal/service/recommendation.go
   │     (business logic, cache-or-generate, worker pool, bulk prefetch coordination)
   ├── internal/repository/user.go
   │     (GetUserByID, GetUsersByIDs bulk, GetUsersPaginated)
   ├── internal/repository/content.go
   │     (GetCandidateContent, GetWatchHistory single, GetWatchHistoryBulk)
   ├── internal/model/scorer.go
   │     (scoring formula, 30-50ms sleep sim, 1.5% failure sim, content exclusion)
   ├── internal/cache/redis.go
   │     (Get, Set, Delete, InvalidateUser, key builder)
   └── internal/domain/types.go
         (User, Content, WatchHistory, Recommendation, BatchResult structs)

□  Migration Scripts
   ├── migrations/001_init.up.sql
   │     (CREATE TABLE IF NOT EXISTS x3 + all 7 indexes IF NOT EXISTS)
   └── migrations/001_init.down.sql
         (DROP TABLE IF EXISTS in FK-safe order)

□  Seed Script
   └── scripts/seed.go
         (rand.NewSource(42), TRUNCATE CASCADE, 20 users, 50 content,
          200+ watch history, power-law popularity, weighted subscription)

□  Docker Configuration
   ├── Dockerfile
   │     (golang:1.26-alpine builder → alpine runtime, multi-stage)
   └── docker-compose.yml
         (app + postgres:15 with healthcheck + redis:7, one-command startup)

□  k6 Test Scripts (3 files)
   ├── tests/k6/load_test.js
   │     (ramp to 100 VUs, 1 min hold, P95<500ms, error<3%)
   ├── tests/k6/batch_test.js
   │     (concurrent batch requests, varying page/limit sizes)
   └── tests/k6/cache_test.js
         (cache_hit_rate custom metric, >70% threshold, 2 min duration)

□  README.md (ครบทุก 5 sections ตาม spec)
   ├── 14.1 Setup Instructions
   │     (prerequisites, docker compose up --build, manual steps, verify curl)
   ├── 14.2 Architecture Overview
   │     (layer table, data flow single + batch, model-DB integration explanation)
   ├── 14.3 Design Decisions
   │     (caching TTL, concurrency approach, error philosophy,
   │      index strategy, scoring weight rationale)
   ├── 14.4 Performance Results
   │     (k6 result tables filled with actual numbers, bottleneck analysis)
   └── 14.5 Trade-offs & Future Improvements
         (known limitations, scalability, proposed enhancements)
```

---

## 18. Evaluation Criteria

| Criterion | Weight | Key Focus | Evaluator จะตรวจ |
|---|---|---|---|
| **System Architecture** | **25%** | Layer separation, DI, no monolithic | Handler ไม่มี SQL, Service ไม่มี HTTP, interface ชัดเจน |
| **Database Design** | **15%** | Schema, indexes, no N+1 | Bulk query ใน batch, FK ถูก, index ครบ 7 ตัว |
| **API Implementation** | **15%** | Endpoint correctness, error codes | JSON fields ครบ spec, error status ถูก |
| **Model Integration** | **15%** | DB-driven scoring, filtering | Score ใช้ข้อมูลจาก DB จริง, watched content excluded |
| **Caching Strategy** | **10%** | Implementation, TTL, invalidation | cache_hit field ใน response, TTL กำหนดได้, invalidation มี |
| **Performance** | **10%** | k6 results, latency, cache hit rate | k6 ผ่าน threshold ทุก metric, cache hit > 70% |
| **Code Quality** | **10%** | Readability, Go best practices | godoc comments, error wrapping, no magic numbers, clean naming |

---

## Quick Reference Card

```
┌──────────────────────────────────────────────────────────┐
│                    QUICK REFERENCE                       │
├──────────────────────────────────────────────────────────┤
│ ENDPOINTS                                                │
│   GET /users/{user_id}/recommendations?limit=10          │
│   GET /recommendations/batch?page=1&limit=20             │
├──────────────────────────────────────────────────────────┤
│ CACHE KEY                                                │
│   rec:user:{user_id}:limit:{limit}   TTL: 10 min        │
│   Invalidate: DEL rec:user:{id}:limit:* on new watch     │
├──────────────────────────────────────────────────────────┤
│ SCORING FORMULA                                          │
│   score = (popularity_score  x 0.40)                     │
│          + (genre_preference x 0.35)                     │
│          + (recency_factor   x 0.15)                     │
│          + (random_noise     x 0.10)                     │
│                                                          │
│   recency = 1.0 / (1.0 + days_since_creation / 365.0)   │
├──────────────────────────────────────────────────────────┤
│ MODEL SIMULATION                                         │
│   Latency : time.Sleep(30-50ms) per request              │
│   Failure : 1.5% random → HTTP 503 model_unavailable     │
├──────────────────────────────────────────────────────────┤
│ SEED                                                     │
│   rand.NewSource(42) — deterministic always              │
│   Users: 20 | Content: 50 | History: 200+               │
│   TRUNCATE ... RESTART IDENTITY CASCADE first            │
├──────────────────────────────────────────────────────────┤
│ BATCH — CRITICAL (avoid N+1)                             │
│   1. Bulk prefetch: GetUsersByIDs()    — 1 query         │
│   2. Bulk prefetch: GetWatchHistoryBulk() — 1 query      │
│   3. THEN fan-out to worker pool (10 goroutines)         │
│   4. Per-user failure does NOT stop batch                │
├──────────────────────────────────────────────────────────┤
│ CONTENT FILTERING (mandatory)                            │
│   WHERE id NOT IN (watched by user)   — always           │
│   Subscription tier filter            — optional         │
│   Geographic availability filter      — optional         │
├──────────────────────────────────────────────────────────┤
│ STARTUP                                                  │
│   docker compose up --build                              │
│   Sequence: migrate → (seed if empty) → HTTP :8080       │
└──────────────────────────────────────────────────────────┘
```

---

*Build layer by layer: Domain → Repository → Model → Cache → Service → Handler 🚀*
