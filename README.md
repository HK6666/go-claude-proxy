# AI API Proxy Gateway

多模型 API 代理网关，自动在 Claude、OpenAI、Codex、Gemini 四种 API 格式之间进行双向转换，支持流式（SSE）和非流式请求。通过 SQLite 记录全量请求审计和 Token 用量/成本。

## 快速开始

### 环境要求

- Go 1.25+（或 Docker）
- SQLite（通过 `go-sqlite3` CGO 绑定，静态编译时可 `CGO_ENABLED=0`）

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `GLM_API_KEY` | 内置测试 Key | 上游 API 密钥 |
| `GLM_BASE_URL` | `https://voyage.prod.telepub.cn/voyage/api` | 上游 API 基地址 |
| `PROXY_PORT` | `27659` | 监听端口 |

支持 `.env` 文件自动加载（启动时读取工作目录下 `.env`）。

### 编译运行

```bash
# 编译
go build -o proxy cmd/proxy/main.go

# 生产环境静态编译
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o proxy cmd/proxy/main.go

# 运行
./proxy
```

### Docker 部署

```bash
docker-compose -f docker-compose.proxy.yml up -d
```

### 测试

```bash
go test ./...                    # 全部测试
go test ./internal/pricing/...   # 单包测试
```

## API 端点

### 代理端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/` | 健康检查，返回 `{"status":"ok"}` |
| POST | `/v1/messages` | 主代理端点，接收 Claude 格式请求 |

客户端以 Claude API 格式发送请求到 `/v1/messages`，代理自动转换为 OpenAI 格式转发到上游 GLM API，再将响应转换回 Claude 格式返回。

### 管理端点（完整版）

完整版包含 Admin API（`/admin/*`），提供 Provider、Route、Project、Session、RetryConfig 等实体的 CRUD 管理。

## 项目结构

```
go-claude-proxy/
├── cmd/proxy/
│   ├── main.go                  # 入口，HTTP 服务启动
│   └── Dockerfile               # 多阶段 Docker 构建
├── internal/
│   ├── adapter/
│   │   ├── client/              # 客户端类型检测（Claude/OpenAI/Codex/Gemini）
│   │   └── provider/
│   │       ├── custom/          # 通用 HTTP 代理适配器
│   │       └── antigravity/     # Google Antigravity（Gemini）适配器
│   ├── context/                 # 类型化 Context Key 管理
│   ├── converter/               # 12 个双向格式转换器 + SSE 流处理
│   ├── cooldown/                # Provider 故障冷却与退避
│   ├── domain/                  # 领域模型定义
│   ├── event/                   # WebSocket 事件广播
│   ├── executor/                # 请求执行 + 重试逻辑
│   ├── handler/                 # HTTP Handler（代理 + Admin CRUD）
│   ├── pricing/                 # Token 成本计算（支持缓存定价分层）
│   ├── repository/
│   │   ├── cached/              # 内存缓存包装层
│   │   └── sqlite/              # SQLite 持久化层
│   ├── router/                  # 路由匹配 + 策略选择
│   ├── service/                 # 业务逻辑层
│   └── usage/                   # Token 用量提取（多格式）
├── docker-compose.proxy.yml
├── CLAUDE.md
├── DESIGN.md                    # 详细设计文档
└── go.mod
```

## 核心请求流程

```
HTTP 请求
  → ClientAdapter（检测客户端格式：Claude/OpenAI/Codex/Gemini）
  → Router（选择路由：priority 或 weighted_random 策略）
  → Executor（执行请求 + 指数退避重试）
  → ProviderAdapter（格式转换 + 代理转发）
  → 上游 API
  → 响应格式逆向转换
  → 返回客户端
```

## 支持的格式转换

12 个双向转换器覆盖四种 API 格式的所有组合：

```
Claude  ⇄  OpenAI
Claude  ⇄  Gemini
Claude  ⇄  Codex
OpenAI  ⇄  Gemini
OpenAI  ⇄  Codex
Gemini  ⇄  Codex
```

每个转换器同时支持请求转换和响应转换（含流式 SSE chunk 逐块转换）。

## 依赖

| 库 | 用途 |
|----|------|
| `github.com/mattn/go-sqlite3` | SQLite 数据库驱动 |
| `github.com/google/uuid` | UUID 生成 |
| `github.com/gorilla/websocket` | WebSocket 支持 |
