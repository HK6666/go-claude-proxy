# 设计文档 — AI API Proxy Gateway

本文档面向 **Python 重写**场景，详细描述系统架构、数据流、核心算法和关键实现细节。

---

## 1. 整体架构

### 1.1 设计原则

- **单实例部署**：所有配置缓存在内存，SQLite 作为唯一存储，不依赖 Redis 等外部服务
- **直接格式转换**：4 种 API 格式之间使用 12 个离散转换器，不经过中间规范格式（避免信息丢失）
- **全量审计**：每个请求和每次上游尝试都记录到数据库
- **Context 传播**：通过 Go Context（Python 可用 contextvars）在请求生命周期中传递元数据

### 1.2 请求生命周期

```
┌─────────┐     ┌───────────────┐     ┌────────┐     ┌──────────┐     ┌─────────────────┐     ┌──────────┐
│  客户端  │────▶│ ClientAdapter │────▶│ Router │────▶│ Executor │────▶│ ProviderAdapter │────▶│ 上游 API │
│         │◀────│  检测格式      │◀────│ 选路由  │◀────│ 重试控制  │◀────│  转换+代理      │◀────│          │
└─────────┘     └───────────────┘     └────────┘     └──────────┘     └─────────────────┘     └──────────┘
```

---

## 2. 客户端类型检测（ClientAdapter）

**文件**：`internal/adapter/client/adapter.go`

### 2.1 检测策略（两层）

**第一层：URL 路径匹配**（快速路径）

| URL 路径模式 | 客户端类型 |
|-------------|-----------|
| `/v1/messages` | Claude |
| `/responses` | Codex |
| `/v1/chat/completions` | OpenAI |
| `/v1beta/models/` 或 `/v1internal/models/` | Gemini |

**第二层：请求体分析**（回退方案）

按以下优先级依次检查请求 JSON 的字段：

1. 有 `contents` 字段且无 `request` 字段 → **Gemini**
2. 有 `request` 字段 → **Gemini**（CLI 信封格式）
3. 有 `input` 字段 → **Codex**
4. 有 `messages` + 顶层 `system` 字段 → **Claude**
5. 有 `messages` → **OpenAI**

### 2.2 Session ID 生成

优先级：
1. 请求体 `metadata.session_id`（Claude 格式特有）
2. 请求头 `X-Session-Id`
3. 用 SHA256 哈希 `Authorization + User-Agent + RemoteIP` 生成确定性 ID

### 2.3 流式检测

- Gemini：检查 URL 路径是否包含 `streamGenerateContent`
- Claude/OpenAI：检查请求体 `stream: true`

---

## 3. 路由系统（Router）

**文件**：`internal/router/router.go`

### 3.1 路由匹配流程

```python
# 伪代码
def match_routes(client_type, project_id):
    routes = get_all_enabled_routes()

    # 1. 按客户端类型过滤
    routes = [r for r in routes if r.client_type == client_type or r.client_type == ""]

    # 2. 按项目过滤（项目专属路由优先于全局路由）
    project_routes = [r for r in routes if r.project_id == project_id]
    global_routes = [r for r in routes if r.project_id is None]
    routes = project_routes if project_routes else global_routes

    # 3. 排除冷却中的 Provider
    routes = [r for r in routes if not cooldown_manager.is_cooling(r.provider_id, client_type)]

    # 4. 排序策略
    if strategy == "priority":
        routes.sort(key=lambda r: r.position)  # position 升序
    elif strategy == "weighted_random":
        random.shuffle(routes)  # 简化实现：随机打乱

    return routes
```

### 3.2 模型映射三层解析

```
RequestModel（客户端原始模型名）
    ↓ Route.model_mapping[request_model] 存在？
MappedModel（路由级映射）
    ↓ Provider.model_mapping[request_model] 存在？
MappedModel（Provider 级映射）
    ↓ 都不存在？
使用原始 RequestModel
```

**Python 实现建议**：用 `dict.get()` 链式查找即可。

---

## 4. 执行器（Executor）

**文件**：`internal/executor/executor.go`

### 4.1 执行流程

```python
# 伪代码
def execute(ctx, request):
    matched_routes = router.match(ctx.client_type, ctx.project_id)
    proxy_request = create_proxy_request(ctx)  # 写入数据库

    for route in matched_routes:
        mapped_model = resolve_model(route, ctx.request_model)
        retry_config = route.retry_config or provider.retry_config or default_retry_config

        for attempt in range(retry_config.max_retries + 1):
            upstream_attempt = create_upstream_attempt(proxy_request, route)

            try:
                result = provider_adapter.execute(ctx, route.provider, mapped_model)
                # 成功
                upstream_attempt.status = "COMPLETED"
                extract_usage_and_cost(upstream_attempt, result)
                proxy_request.status = "COMPLETED"
                return result

            except ProxyError as e:
                upstream_attempt.status = "FAILED"

                if not e.retryable:
                    break  # 跳到下一个路由

                if attempt < retry_config.max_retries:
                    wait = retry_config.initial_interval * (retry_config.backoff_rate ** attempt)
                    wait = min(wait, retry_config.max_interval)
                    time.sleep(wait)
                    continue

                # 记录冷却
                cooldown_manager.record_failure(route.provider_id, ctx.client_type, e.reason)
                break  # 跳到下一个路由

    proxy_request.status = "FAILED"
    return error_response
```

### 4.2 退避算法

```
wait = initial_interval_ms × (backoff_rate ^ attempt_number)
if wait > max_interval_ms:
    wait = max_interval_ms
```

默认配置建议：
- `max_retries`: 2
- `initial_interval_ms`: 1000
- `backoff_rate`: 2.0
- `max_interval_ms`: 10000

---

## 5. 冷却管理（Cooldown）

**文件**：`internal/cooldown/manager.go`

### 5.1 冷却键结构

```python
CooldownKey = (provider_id: int, client_type: str)
# client_type="" 表示全局冷却，"claude"/"openai" 等表示特定类型
```

### 5.2 冷却策略

| 失败原因 | 策略 | 计算方式 |
|---------|------|---------|
| `server_error`（5xx） | 线性递增 | 1min, 2min, 3min... 最大 10min |
| `network_error` | 指数退避 | 1min, 2min, 4min, 8min... 最大 30min |
| `quota_exhausted` | 固定时长 | 1 小时（回退值，优先用 API 返回的重置时间） |
| `rate_limit_exceeded` | 固定时长 | 1 分钟（回退值，优先用 Retry-After 头） |
| `concurrent_limit` | 固定时长 | 10 秒 |

### 5.3 检查逻辑

```python
def is_cooling(provider_id, client_type):
    global_until = get_cooldown(provider_id, "")
    specific_until = get_cooldown(provider_id, client_type)
    cooldown_until = max(global_until, specific_until)
    return datetime.now() < cooldown_until
```

---

## 6. 格式转换器系统（Converter）

**文件**：`internal/converter/` 目录

### 6.1 架构

```python
# 注册表模式
converter_registry = {}

def register(from_type, to_type, request_transformer, response_transformer):
    converter_registry[(from_type, to_type)] = (request_transformer, response_transformer)

# 使用
def transform_request(from_type, to_type, body, model, stream):
    req_transformer, _ = converter_registry[(from_type, to_type)]
    return req_transformer.transform(body, model, stream)

def transform_response(from_type, to_type, body):
    _, resp_transformer = converter_registry[(from_type, to_type)]
    return resp_transformer.transform(body)

def transform_stream_chunk(from_type, to_type, chunk, state):
    _, resp_transformer = converter_registry[(from_type, to_type)]
    return resp_transformer.transform_chunk(chunk, state)
```

### 6.2 12 个转换器

| 文件 | 方向 | 关键转换点 |
|------|------|-----------|
| `claude_to_openai.go` | Claude→OpenAI | `system` 提升为 message[0]；`tool_use` → `tool_calls`；content blocks → `content` 字符串 |
| `openai_to_claude.go` | OpenAI→Claude | `tool_calls` → `tool_use` blocks；`image_url` data URI 转换 |
| `claude_to_gemini.go` | Claude→Gemini | Content blocks → Gemini `parts`；role 映射（assistant→model） |
| `gemini_to_claude.go` | Gemini→Claude | Gemini `parts` → content blocks；`candidates[0]` 提取 |
| `claude_to_codex.go` | Claude→Codex | messages 数组 → `input` 字段 |
| `codex_to_claude.go` | Codex→Claude | Codex response → Claude 格式 |
| `openai_to_gemini.go` | OpenAI→Gemini | messages → parts；function_call 转换 |
| `gemini_to_openai.go` | Gemini→OpenAI | parts → messages；choices 包装 |
| `openai_to_codex.go` | OpenAI→Codex | OpenAI → Codex Responses API |
| `codex_to_openai.go` | Codex→OpenAI | Codex → OpenAI Chat Completions |
| `gemini_to_codex.go` | Gemini→Codex | Gemini → Codex |
| `codex_to_gemini.go` | Codex→Gemini | Codex → Gemini |

### 6.3 Claude ⇄ OpenAI 转换详解（最常用）

**请求转换 Claude → OpenAI**：

```python
def claude_to_openai_request(claude_body, model, stream):
    openai = {
        "model": model,
        "stream": stream,
        "messages": [],
    }

    # 1. system 消息处理
    if "system" in claude_body:
        system = claude_body["system"]
        if isinstance(system, str):
            openai["messages"].append({"role": "system", "content": system})
        elif isinstance(system, list):
            # Claude 的 system 可以是 content block 数组
            text = " ".join(b["text"] for b in system if b["type"] == "text")
            openai["messages"].append({"role": "system", "content": text})

    # 2. messages 转换
    for msg in claude_body["messages"]:
        openai_msg = {"role": msg["role"]}

        if isinstance(msg["content"], str):
            openai_msg["content"] = msg["content"]
        elif isinstance(msg["content"], list):
            # 处理 content blocks
            text_parts = []
            tool_calls = []
            for block in msg["content"]:
                if block["type"] == "text":
                    text_parts.append(block["text"])
                elif block["type"] == "tool_use":
                    tool_calls.append({
                        "id": block["id"],
                        "type": "function",
                        "function": {
                            "name": block["name"],
                            "arguments": json.dumps(block["input"]),
                        }
                    })
                elif block["type"] == "tool_result":
                    # tool_result 在 OpenAI 中是独立的 tool message
                    openai["messages"].append({
                        "role": "tool",
                        "tool_call_id": block["tool_use_id"],
                        "content": extract_text(block["content"]),
                    })
                    continue
                elif block["type"] == "image":
                    # Base64 图片 → OpenAI image_url 格式
                    text_parts.append({
                        "type": "image_url",
                        "image_url": {
                            "url": f"data:{block['source']['media_type']};base64,{block['source']['data']}"
                        }
                    })

            if tool_calls:
                openai_msg["tool_calls"] = tool_calls
            openai_msg["content"] = " ".join(text_parts) if all(isinstance(t, str) for t in text_parts) else text_parts

        openai["messages"].append(openai_msg)

    # 3. 参数映射
    if "max_tokens" in claude_body:
        openai["max_tokens"] = claude_body["max_tokens"]
    if "temperature" in claude_body:
        openai["temperature"] = claude_body["temperature"]
    if "top_p" in claude_body:
        openai["top_p"] = claude_body["top_p"]
    if "tools" in claude_body:
        openai["tools"] = convert_claude_tools_to_openai(claude_body["tools"])

    return openai
```

**响应转换 OpenAI → Claude**：

```python
def openai_to_claude_response(openai_body):
    choice = openai_body["choices"][0]
    msg = choice["message"]

    content_blocks = []

    # 文本内容
    if msg.get("content"):
        content_blocks.append({"type": "text", "text": msg["content"]})

    # 工具调用
    if msg.get("tool_calls"):
        for tc in msg["tool_calls"]:
            content_blocks.append({
                "type": "tool_use",
                "id": tc["id"],
                "name": tc["function"]["name"],
                "input": json.loads(tc["function"]["arguments"]),
            })

    # stop_reason 映射
    stop_reason_map = {
        "stop": "end_turn",
        "length": "max_tokens",
        "tool_calls": "tool_use",
    }

    claude = {
        "id": f"msg_{openai_body['id']}",
        "type": "message",
        "role": "assistant",
        "content": content_blocks,
        "model": openai_body["model"],
        "stop_reason": stop_reason_map.get(choice["finish_reason"], "end_turn"),
        "usage": {
            "input_tokens": openai_body["usage"]["prompt_tokens"],
            "output_tokens": openai_body["usage"]["completion_tokens"],
        },
    }
    return claude
```

### 6.4 SSE 流式转换

**SSE 解析**：

```python
def parse_sse(text):
    """解析 SSE 文本，返回 (events, remaining_buffer)"""
    events = []
    lines = text.split("\n")
    current_event = ""
    current_data = ""

    for line in lines:
        if line.startswith("event: "):
            current_event = line[7:]
        elif line.startswith("data: "):
            current_data = line[6:]
        elif line == "":
            if current_data:
                if current_data != "[DONE]":
                    events.append({"event": current_event, "data": json.loads(current_data)})
                current_event = ""
                current_data = ""

    # 返回未完成的部分作为 buffer
    remaining = ... # 最后一个不完整的块
    return events, remaining
```

**TransformState（流式状态机）**：

```python
class TransformState:
    def __init__(self):
        self.message_id = generate_id()
        self.current_index = 0
        self.current_block_type = ""     # "text" / "thinking" / "tool_use"
        self.tool_calls = {}             # index → ToolCallState
        self.buffer = ""                 # SSE 行缓冲（跨 chunk 拼接）
        self.usage = None                # 累积的 token 用量
        self.stop_reason = ""
        self.has_thinking = False
```

**流式转换核心逻辑（OpenAI SSE → Claude SSE）**：

```python
def transform_stream_chunk(chunk_bytes, state):
    # 1. 将新数据追加到缓冲
    state.buffer += chunk_bytes.decode()

    # 2. 解析出完整的 SSE 事件
    events, state.buffer = parse_sse(state.buffer)

    output_lines = []

    for event in events:
        delta = event["data"]["choices"][0]["delta"]

        # 3. 文本增量
        if "content" in delta and delta["content"]:
            if state.current_block_type != "text":
                # 开启新的 text block
                output_lines.append(sse_event("content_block_start", {
                    "type": "content_block_start",
                    "index": state.current_index,
                    "content_block": {"type": "text", "text": ""}
                }))
                state.current_block_type = "text"

            output_lines.append(sse_event("content_block_delta", {
                "type": "content_block_delta",
                "index": state.current_index,
                "delta": {"type": "text_delta", "text": delta["content"]}
            }))

        # 4. 工具调用增量
        if "tool_calls" in delta:
            for tc in delta["tool_calls"]:
                idx = tc["index"]
                if idx not in state.tool_calls:
                    # 新工具调用开始
                    state.current_index += 1
                    state.tool_calls[idx] = {
                        "id": tc["id"],
                        "name": tc["function"]["name"],
                        "arguments": "",
                    }
                    output_lines.append(sse_event("content_block_start", {
                        "type": "content_block_start",
                        "index": state.current_index,
                        "content_block": {
                            "type": "tool_use",
                            "id": tc["id"],
                            "name": tc["function"]["name"],
                            "input": {},
                        }
                    }))

                if tc.get("function", {}).get("arguments"):
                    state.tool_calls[idx]["arguments"] += tc["function"]["arguments"]
                    output_lines.append(sse_event("content_block_delta", {
                        "type": "content_block_delta",
                        "index": state.current_index,
                        "delta": {
                            "type": "input_json_delta",
                            "partial_json": tc["function"]["arguments"],
                        }
                    }))

        # 5. finish_reason
        if event["data"]["choices"][0].get("finish_reason"):
            state.stop_reason = map_stop_reason(event["data"]["choices"][0]["finish_reason"])

        # 6. usage（通常在最后一个事件）
        if "usage" in event["data"] and event["data"]["usage"]:
            state.usage = event["data"]["usage"]

    return "\n".join(output_lines).encode()
```

---

## 7. Provider 适配器

**文件**：`internal/adapter/provider/`

### 7.1 CustomAdapter（通用 HTTP 代理）

这是最核心的适配器，处理流程：

```python
def execute(ctx, provider, response_writer):
    client_type = ctx.client_type
    target_type = determine_target_type(provider, client_type)

    # 1. 请求转换（如果需要）
    if client_type != target_type:
        body = converter_registry.transform_request(client_type, target_type, ctx.body, ctx.mapped_model, ctx.is_stream)
    else:
        body = replace_model_in_body(ctx.body, ctx.mapped_model)

    # 2. 构建上游 URL
    url = provider.base_url + get_path_for_type(target_type)

    # 3. 设置认证头
    headers = {
        "Content-Type": "application/json",
    }
    if target_type == "claude":
        headers["x-api-key"] = provider.api_key
        headers["anthropic-version"] = "2023-06-01"
    elif target_type == "gemini":
        headers["x-goog-api-key"] = provider.api_key
    else:  # openai / codex
        headers["Authorization"] = f"Bearer {provider.api_key}"

    # 4. 发送请求
    response = http_client.post(url, body, headers)

    # 5. 响应处理
    if ctx.is_stream:
        handle_stream(response, response_writer, target_type, client_type)
    else:
        resp_body = response.read()
        if client_type != target_type:
            resp_body = converter_registry.transform_response(target_type, client_type, resp_body)
        response_writer.write(resp_body)
```

### 7.2 错误分类

```python
def classify_error(status_code, response_body):
    if status_code == 429:
        if "quota" in response_body.lower():
            return "quota_exhausted", True   # (reason, retryable)
        return "rate_limit_exceeded", True
    elif 400 <= status_code < 500:
        return "client_error", False         # 不可重试
    elif 500 <= status_code < 600:
        return "server_error", True          # 可重试
    else:
        return "network_error", True
```

---

## 8. 数据库设计

### 8.1 完整 Schema

```sql
-- ========================
-- 核心配置表
-- ========================

CREATE TABLE providers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    type            TEXT NOT NULL,              -- "custom" | "antigravity"
    name            TEXT NOT NULL,
    config_json     TEXT,                       -- JSON: {base_url, api_key, supported_types, model_mapping, ...}
    supported_client_types TEXT,                -- 逗号分隔: "claude,openai"
    deleted_at      DATETIME
);

CREATE TABLE projects (
    id    INTEGER PRIMARY KEY AUTOINCREMENT,
    name  TEXT NOT NULL
);

CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT UNIQUE NOT NULL,           -- 唯一会话标识
    client_type TEXT NOT NULL,
    project_id  INTEGER REFERENCES projects(id)
);
CREATE INDEX idx_sessions_session_id ON sessions(session_id);

CREATE TABLE routes (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled           BOOLEAN DEFAULT TRUE,
    native            BOOLEAN DEFAULT FALSE,    -- 是否原生格式（不需转换）
    project_id        INTEGER REFERENCES projects(id),  -- NULL = 全局
    client_type       TEXT,                     -- 匹配的客户端类型，空 = 全部
    provider_id       INTEGER NOT NULL REFERENCES providers(id),
    position          INTEGER DEFAULT 0,        -- 优先级排序（小数优先）
    retry_config_id   INTEGER REFERENCES retry_configs(id),
    model_mapping_json TEXT                     -- JSON: {"claude-3-5-sonnet": "gpt-4o", ...}
);
CREATE INDEX idx_routes_project_client ON routes(project_id, client_type);

CREATE TABLE retry_configs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    name                TEXT NOT NULL,
    is_default          BOOLEAN DEFAULT FALSE,  -- 全局默认配置
    max_retries         INTEGER DEFAULT 2,
    initial_interval_ms INTEGER DEFAULT 1000,
    backoff_rate        REAL DEFAULT 2.0,
    max_interval_ms     INTEGER DEFAULT 10000
);

CREATE TABLE routing_strategies (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER REFERENCES projects(id),  -- NULL = 全局
    type        TEXT NOT NULL,                     -- "priority" | "weighted_random"
    config_json TEXT                               -- 策略参数 JSON
);

-- ========================
-- 审计追踪表（不缓存）
-- ========================

CREATE TABLE proxy_requests (
    id                              INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id                     TEXT,           -- 服务实例标识
    request_id                      TEXT,           -- 请求唯一 ID
    session_id                      TEXT,
    client_type                     TEXT,
    request_model                   TEXT,           -- 客户端请求的模型
    response_model                  TEXT,           -- 上游实际返回的模型
    start_time                      DATETIME,
    end_time                        DATETIME,
    duration_ms                     INTEGER,
    is_stream                       BOOLEAN,
    status                          TEXT,           -- "IN_PROGRESS" | "COMPLETED" | "FAILED"
    request_info_json               TEXT,           -- 请求摘要 JSON
    response_info_json              TEXT,           -- 响应摘要 JSON
    error                           TEXT,
    input_token_count               INTEGER DEFAULT 0,
    output_token_count              INTEGER DEFAULT 0,
    cache_read_count                INTEGER DEFAULT 0,
    cache_write_count               INTEGER DEFAULT 0,
    cache_5m_write_count            INTEGER DEFAULT 0,
    cache_1h_write_count            INTEGER DEFAULT 0,
    cost                            INTEGER DEFAULT 0,  -- 微美元 (micro USD)
    route_id                        INTEGER,
    provider_id                     INTEGER,
    proxy_upstream_attempt_count    INTEGER DEFAULT 0,
    final_proxy_upstream_attempt_id INTEGER
);
CREATE INDEX idx_proxy_requests_session ON proxy_requests(session_id);

CREATE TABLE proxy_upstream_attempts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    status              TEXT,               -- "IN_PROGRESS" | "COMPLETED" | "FAILED" | "CANCELLED"
    proxy_request_id    INTEGER NOT NULL REFERENCES proxy_requests(id),
    route_id            INTEGER,
    provider_id         INTEGER,
    is_stream           BOOLEAN,
    request_info_json   TEXT,
    response_info_json  TEXT,
    input_token_count   INTEGER DEFAULT 0,
    output_token_count  INTEGER DEFAULT 0,
    cache_read_count    INTEGER DEFAULT 0,
    cache_write_count   INTEGER DEFAULT 0,
    cache_5m_write_count  INTEGER DEFAULT 0,
    cache_1h_write_count  INTEGER DEFAULT 0,
    cost                INTEGER DEFAULT 0
);

-- ========================
-- 故障追踪表
-- ========================

CREATE TABLE cooldowns (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL,
    client_type TEXT NOT NULL DEFAULT '',       -- 空 = 全局
    until_time  DATETIME NOT NULL,
    reason      TEXT
);
CREATE UNIQUE INDEX idx_cooldowns_provider_client ON cooldowns(provider_id, client_type);
CREATE INDEX idx_cooldowns_until ON cooldowns(until_time);

CREATE TABLE failure_counts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id     INTEGER NOT NULL,
    client_type     TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL,
    count           INTEGER DEFAULT 0,
    last_failure_at DATETIME
);
CREATE UNIQUE INDEX idx_failure_counts_provider_client_reason ON failure_counts(provider_id, client_type, reason);

-- ========================
-- 系统配置
-- ========================

CREATE TABLE system_settings (
    key   TEXT PRIMARY KEY,
    value TEXT
);
```

### 8.2 缓存策略

- **配置表**（providers, routes, sessions, retry_configs, routing_strategies）：内存缓存，写入时自动刷新
- **审计表**（proxy_requests, proxy_upstream_attempts）：**不缓存**，直接读写 SQLite
- **冷却表**（cooldowns, failure_counts）：内存缓存 + SQLite 持久化（重启恢复）

**Python 实现建议**：配置表可用 `functools.lru_cache` 或自定义 dict 缓存，mutation 时清缓存。

---

## 9. Token 用量提取

**文件**：`internal/usage/extractor.go`

### 9.1 多格式提取

```python
def extract_usage(response_body, client_type):
    data = json.loads(response_body)

    if client_type == "claude":
        usage = data.get("usage", {})
        return {
            "input_tokens": usage.get("input_tokens", 0),
            "output_tokens": usage.get("output_tokens", 0),
            "cache_read": usage.get("cache_read_input_tokens", 0),
            "cache_creation": usage.get("cache_creation_input_tokens", 0),
        }

    elif client_type == "openai":
        usage = data.get("usage", {})
        return {
            "input_tokens": usage.get("prompt_tokens", 0),
            "output_tokens": usage.get("completion_tokens", 0),
        }

    elif client_type == "gemini":
        usage = data.get("usageMetadata", {})
        return {
            "input_tokens": usage.get("inputTokenCount", 0) or usage.get("promptTokenCount", 0),
            "output_tokens": usage.get("outputTokenCount", 0) or usage.get("candidatesTokenCount", 0),
            "cache_read": usage.get("cachedContentTokenCount", 0),
        }

    elif client_type == "codex":
        # Codex Response API 嵌套在 response 字段内
        usage = data.get("response", {}).get("usage", data.get("usage", {}))
        return {
            "input_tokens": usage.get("input_tokens", 0),
            "output_tokens": usage.get("output_tokens", 0),
        }
```

### 9.2 SSE 流式用量提取

流式模式下，token 用量通常在最后一个 SSE 事件中：

- **Claude**：`type: "message_delta"` 事件包含最终 usage
- **OpenAI**：最后一个 chunk 的 `usage` 字段
- **Codex**：`type: "response.completed"` 事件

```python
def extract_usage_from_sse(sse_text, client_type):
    """遍历所有 SSE 行，提取最后一个包含 usage 的事件"""
    last_usage = None
    for line in sse_text.split("\n"):
        if not line.startswith("data: ") or line == "data: [DONE]":
            continue
        data = json.loads(line[6:])
        usage = extract_usage_from_event(data, client_type)
        if usage:
            last_usage = usage
    return last_usage
```

---

## 10. 定价计算

**文件**：`internal/pricing/calculator.go`

### 10.1 定价模型

```python
MODEL_PRICING = {
    "claude-sonnet-4": {
        "input_price_micro": 3_000_000,      # $3 / 1M tokens（微美元）
        "output_price_micro": 15_000_000,     # $15 / 1M tokens
        "has_1m_context": True,
        "context_1m_threshold": 200_000,      # 超过 20 万 token 后加价
        "input_premium": (2, 1),              # 2x
        "output_premium": (3, 2),             # 1.5x
    },
    # ... 其他模型
}
```

### 10.2 成本计算（纯整数运算避免浮点误差）

```python
def calculate_cost(model, input_tokens, output_tokens, cache_read, cache_5m_write, cache_1h_write):
    pricing = find_pricing(model)  # 前缀匹配，最长优先
    total = 0

    # 输入成本（可能有分层）
    if pricing.get("has_1m_context"):
        threshold = pricing["context_1m_threshold"]
        if input_tokens <= threshold:
            total += input_tokens * pricing["input_price_micro"] // 1_000_000
        else:
            base = threshold * pricing["input_price_micro"] // 1_000_000
            premium_num, premium_denom = pricing["input_premium"]
            excess = (input_tokens - threshold) * pricing["input_price_micro"] * premium_num // (1_000_000 * premium_denom)
            total += base + excess
    else:
        total += input_tokens * pricing["input_price_micro"] // 1_000_000

    # 输出成本（同理分层）
    total += output_cost  # 类似逻辑

    # 缓存成本
    total += cache_read * (pricing["input_price_micro"] // 10) // 1_000_000       # 10% of input price
    total += cache_5m_write * (pricing["input_price_micro"] * 5 // 4) // 1_000_000  # 125% of input price
    total += cache_1h_write * (pricing["input_price_micro"] * 2) // 1_000_000       # 200% of input price

    return total  # 微美元
```

---

## 11. Python 重写建议

### 11.1 技术选型

| Go 组件 | Python 建议 | 说明 |
|---------|------------|------|
| `net/http` | FastAPI + uvicorn | 异步 HTTP，自带 SSE 支持 |
| `context.Context` | `contextvars` | 请求级别变量传播 |
| `go-sqlite3` | `aiosqlite` + `sqlalchemy` | 异步 SQLite |
| `sync.RWMutex` | `asyncio.Lock` | 异步锁 |
| `gorilla/websocket` | FastAPI WebSocket | 内置支持 |
| `encoding/json` | `orjson` | 高性能 JSON |
| SSE 流式 | `sse-starlette` 或 `StreamingResponse` | FastAPI 流式响应 |
| HTTP Client | `httpx` (async) | 异步 HTTP 客户端，支持流式 |

### 11.2 项目结构建议

```
python-proxy/
├── main.py                      # FastAPI 入口
├── config.py                    # 环境变量 + .env 加载
├── adapters/
│   ├── client.py                # 客户端类型检测
│   └── providers/
│       ├── custom.py            # 通用 HTTP 代理
│       └── antigravity.py       # Google Antigravity
├── converters/
│   ├── registry.py              # 转换器注册表
│   ├── sse.py                   # SSE 解析 + TransformState
│   ├── claude_to_openai.py
│   ├── openai_to_claude.py
│   └── ...                      # 其余 10 个转换器
├── router/
│   ├── router.py                # 路由匹配
│   └── cooldown.py              # 冷却管理
├── executor/
│   └── executor.py              # 请求执行 + 重试
├── models/
│   └── domain.py                # Pydantic 领域模型
├── repositories/
│   ├── cached.py                # 内存缓存层
│   └── sqlite.py                # SQLite 持久化
├── pricing/
│   └── calculator.py            # 成本计算
├── usage/
│   └── extractor.py             # Token 用量提取
└── handlers/
    ├── proxy.py                 # 代理端点
    └── admin.py                 # Admin CRUD
```

### 11.3 关键实现注意事项

1. **SSE 流式转换是最复杂的部分**：需要维护跨 chunk 的状态机，注意 JSON 可能被截断在 chunk 边界处。Python 中用 `httpx.stream()` + `async for` 逐块读取。

2. **格式转换要处理大量边界情况**：tool_use、图片、thinking blocks、多模态内容等。建议先实现 Claude ⇄ OpenAI（最常用），再逐步添加其他格式。

3. **GLM 特殊处理**：上游 GLM API 可能返回 `reasoning_content` 字段（类似 thinking），当前代码会把它丢弃（flatten 为空）。还会主动关闭 thinking 功能以提高性能。

4. **并发安全**：Go 用 `sync.RWMutex` 保护缓存读写，Python asyncio 单线程模型天然避免大部分竞态，但共享状态仍需 `asyncio.Lock`。

5. **成本计算用整数运算**：避免浮点精度问题，所有价格单位为微美元（1 USD = 1,000,000 micro USD）。
