# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

AI API proxy gateway that routes requests across multiple upstream providers with automatic format conversion between Claude, OpenAI, Codex, and Gemini API formats. Tracks all requests, token usage, and costs in SQLite.

## Build & Run

```bash
# Build
go build -o proxy cmd/proxy/main.go

# Production static binary
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o proxy cmd/proxy/main.go

# Run (reads .env file automatically)
./proxy

# Docker
docker-compose -f docker-compose.proxy.yml up -d
```

**Environment variables:** `GLM_API_KEY`, `GLM_BASE_URL` (default: `https://voyage.prod.telepub.cn/voyage/api`), `PROXY_PORT` (default: `27659`)

## Testing

```bash
go test ./...                    # all tests
go test ./internal/pricing/...   # single package
```

## Architecture

### Request Flow

```
HTTP Request → ClientAdapter (detect format) → Router (select route) → Executor (retry logic) → ProviderAdapter (convert & proxy) → Upstream API
```

### Key Packages

- **`internal/adapter/client`** — Detects client type (Claude/OpenAI/Codex/Gemini) from URL patterns and request body structure
- **`internal/adapter/provider`** — Provider adapters: `custom` (generic HTTP proxy) and `antigravity` (Google Antigravity with OAuth2 token management)
- **`internal/converter`** — Registry of 12 bidirectional format converters between all supported API formats. Handles streaming SSE transformation with a state machine
- **`internal/router`** — Route selection with two strategies: `priority` (position-ordered) and `weighted_random`. Integrates cooldown manager to skip failed providers
- **`internal/executor`** — Request execution with configurable retry (exponential backoff). Creates ProxyRequest/ProxyUpstreamAttempt audit records
- **`internal/repository/cached`** — In-memory cache wrappers over SQLite for configuration entities (Provider, Route, Session, etc.). Auto-refreshes on mutations
- **`internal/repository/sqlite`** — SQLite persistence layer. ProxyRequest and ProxyUpstreamAttempt are never cached
- **`internal/handler`** — HTTP handlers for proxy endpoints and admin CRUD APIs
- **`internal/cooldown`** — Per-provider failure tracking with incremental backoff
- **`internal/pricing`** — Token cost calculation with cache-aware pricing tiers

### Model Mapping Resolution

Three-tier: RequestModel (from client) → MappedModel (route or provider mapping) → ResponseModel (from upstream). Route-level mappings override provider-level.

### Design Decisions

- **Single-instance deployment** — all config cached in-memory, SQLite-only storage
- **Direct format conversion** — 12 discrete converters rather than an intermediate canonical format
- **Full audit trail** — every request, attempt, and response recorded to database
- **Context propagation** — typed context keys for ClientType, SessionID, ProjectID, models, streaming state, etc.
