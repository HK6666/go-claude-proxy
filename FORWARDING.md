# 转发代理到底改了什么

本文档说明代理在转发过程中**具体修改了哪些东西**，帮助你理解请求从 Claude Code 到 GLM 上游经历了什么变化。

---

## 一句话总结

代理把 **Claude 格式的请求**改写成 **OpenAI 格式的请求**发给 GLM，再把 **OpenAI 格式的响应**改写回 **Claude 格式**返回给客户端。本质上是一个**格式翻译器**。

---

## 请求方向（客户端 → 上游）

### 1. URL 改变

```
客户端发送:  POST /v1/messages          (Claude API 端点)
实际转发到:  POST {GLM_BASE_URL}/v1/chat/completions  (OpenAI API 端点)
```

### 2. 认证头改变

```
客户端发送:  x-api-key: sk-ant-xxx...          (Claude 格式)
转发变成:    Authorization: Bearer sk-ant-xxx... (OpenAI 格式)
```

代理从客户端请求头提取 key（`x-api-key` 或 `Authorization: Bearer`），转为 OpenAI 的 Bearer Token 格式发给上游。客户端没带 key 则使用 `.env` 中的全局 key。

### 3. 模型名改变

```
客户端发送:  "model": "claude-sonnet-4-20250514"  (任意 Claude 模型名)
转发变成:    "model": "glm-5"                     (统一映射为 GLM 模型)
```

所有模型名都被硬编码映射为 `glm-5`。

### 4. 请求体结构改变

Claude 和 OpenAI 的请求格式差异很大，以下是主要改动点：

#### 4.1 system 消息

```json
// Claude 格式：system 是顶层字段
{
  "system": "你是一个助手",
  "messages": [...]
}

// 转换后 OpenAI 格式：system 变成 messages 数组的第一条
{
  "messages": [
    {"role": "system", "content": "你是一个助手"},
    ...
  ]
}
```

#### 4.2 普通消息

```json
// Claude 格式：content 可以是 content block 数组
{
  "role": "user",
  "content": [
    {"type": "text", "text": "你好"},
    {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "xxx"}}
  ]
}

// 转换后 OpenAI 格式：
{
  "role": "user",
  "content": [
    {"type": "text", "text": "你好"},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,xxx"}}
  ]
}
```

只有一个 text block 时会简化为纯字符串：`"content": "你好"`

#### 4.3 工具调用（Tool Use）

```json
// Claude 格式：tool_use 是 content block
{
  "role": "assistant",
  "content": [
    {
      "type": "tool_use",
      "id": "toolu_xxx",
      "name": "read_file",
      "input": {"path": "/tmp/test.txt"}
    }
  ]
}

// 转换后 OpenAI 格式：tool_calls 是独立字段
{
  "role": "assistant",
  "content": "",
  "tool_calls": [
    {
      "id": "toolu_xxx",
      "type": "function",
      "function": {
        "name": "read_file",
        "arguments": "{\"path\":\"/tmp/test.txt\"}"
      }
    }
  ]
}
```

注意：`input`（对象）→ `arguments`（JSON 字符串）

#### 4.4 工具结果（Tool Result）

```json
// Claude 格式：tool_result 是 user 消息里的 content block
{
  "role": "user",
  "content": [
    {"type": "tool_result", "tool_use_id": "toolu_xxx", "content": "文件内容..."}
  ]
}

// 转换后 OpenAI 格式：独立的 tool 角色消息
{
  "role": "tool",
  "tool_call_id": "toolu_xxx",
  "content": "文件内容..."
}
```

#### 4.5 工具定义（Tools）

```json
// Claude 格式
{
  "tools": [{
    "name": "read_file",
    "description": "读取文件",
    "input_schema": {"type": "object", "properties": {...}}
  }]
}

// 转换后 OpenAI 格式：多套一层 function
{
  "tools": [{
    "type": "function",
    "function": {
      "name": "read_file",
      "description": "读取文件",
      "parameters": {"type": "object", "properties": {...}}
    }
  }]
}
```

#### 4.6 tool_choice 映射

| Claude | OpenAI |
|--------|--------|
| `"any"` | `"required"` |
| `"auto"` | `"auto"` |
| `"none"` | `"none"` |
| `{"type": "tool", "name": "xxx"}` | `{"type": "function", "function": {"name": "xxx"}}` |

#### 4.7 其他参数

直接透传，字段名不变：`max_tokens`、`temperature`、`top_p`、`stop_sequences`→`stop`

### 5. GLM 特殊处理

因为 GLM 不支持 OpenAI 的 `tool` 角色消息，代理做了额外处理：

```json
// 工具调用的 assistant 消息被展平为纯文本：
{"role": "assistant", "content": "分析一下文件\n[Called tool: read_file({\"path\":\"/tmp/test.txt\"})]"}

// tool 结果被转为 user 消息：
{"role": "user", "content": "[Tool result for toolu_xxx]: 文件内容..."}
```

另外还会注入以下字段禁用 GLM 的思考功能（减少开销）：

```json
{
  "include_reasoning": false,
  "extended_thinking": false
}
```

---

## 响应方向（上游 → 客户端）

### 6. 非流式响应转换

```json
// GLM 返回的 OpenAI 格式
{
  "id": "chatcmpl-xxx",
  "choices": [{
    "message": {"role": "assistant", "content": "你好！"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 10, "completion_tokens": 5}
}

// 转换后返回给客户端的 Claude 格式
{
  "id": "chatcmpl-xxx",
  "type": "message",
  "role": "assistant",
  "content": [{"type": "text", "text": "你好！"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}
```

关键映射：
- `choices[0].message.content`（字符串）→ `content`（block 数组）
- `prompt_tokens` → `input_tokens`
- `completion_tokens` → `output_tokens`
- `finish_reason` 映射：`"stop"` → `"end_turn"`，`"length"` → `"max_tokens"`，`"tool_calls"` → `"tool_use"`

### 7. 思考内容处理

如果 GLM 返回了 `<think>...</think>` 标签，代理会提取出来转为 Claude 的 thinking block：

```json
// GLM 返回
{"content": "<think>让我想想...</think>你好！"}

// 转换后
{
  "content": [
    {"type": "thinking", "thinking": "让我想想..."},
    {"type": "text", "text": "你好！"}
  ]
}
```

如果 GLM 返回了 `reasoning_content` 字段（DeepSeek 风格），则直接丢弃。

### 8. 流式（SSE）响应转换

这是最复杂的部分。OpenAI 和 Claude 的 SSE 事件格式完全不同：

```
OpenAI 流式格式:
  data: {"choices":[{"delta":{"content":"你"}}]}
  data: {"choices":[{"delta":{"content":"好"}}]}
  data: {"choices":[{"finish_reason":"stop"}]}
  data: [DONE]

转换后 Claude 流式格式:
  event: message_start
  data: {"type":"message_start","message":{"id":"xxx","role":"assistant",...}}

  event: content_block_start
  data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

  event: content_block_delta
  data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你"}}

  event: content_block_delta
  data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好"}}

  event: content_block_stop
  data: {"type":"content_block_stop","index":0}

  event: message_delta
  data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

  event: message_stop
  data: {"type":"message_stop"}
```

Claude 的 SSE 协议更复杂，需要维护一个**状态机**来跟踪当前处于哪个 block、是否已发送开始/结束事件等。

---

## 总结：改了什么，没改什么

### 改了的

| 项目 | 说明 |
|------|------|
| URL | `/v1/messages` → `/v1/chat/completions` |
| 认证头 | `x-api-key` → `Authorization: Bearer` |
| 模型名 | 任意 Claude 模型 → `glm-5` |
| 请求体结构 | Claude Messages API → OpenAI Chat Completions API |
| 响应体结构 | OpenAI → Claude（含 SSE 流式） |
| 工具调用格式 | Claude tool_use blocks ⇄ OpenAI tool_calls |
| 停止原因 | `end_turn` ⇄ `stop` 等映射 |
| token 字段名 | `input_tokens` ⇄ `prompt_tokens` 等 |
| GLM 特殊 | 禁用 thinking，展平 tool 消息 |

### 没改的

| 项目 | 说明 |
|------|------|
| 消息内容本身 | 文本内容原样传递，不修改任何文字 |
| 图片数据 | 仅格式包装变化（base64 数据不变） |
| 工具参数/结果 | 值不变，只是包装格式变了 |
| temperature 等参数 | 直接透传 |
