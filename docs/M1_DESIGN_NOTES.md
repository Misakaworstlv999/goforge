# M1: Hello LLM — 设计笔记与框架对照

本文档详细解释 GoForge M1 阶段的代码架构，并与 Eino (ByteDance)、tRPC-Agent-Go (Tencent)、ADK-Go (Google) 三个参考框架进行逐层对照。

---

## 1. 整体结构

M1 阶段完成了 Ring 1（LLM Client）的核心实现，包含以下文件：

```
pkg/llm/
├── llm.go           # LLM 接口定义 (25 行)
├── message.go       # 提供商无关的消息类型 (85 行)
├── option.go        # 函数式选项 (54 行)
├── openai/
│   ├── provider.go  # OpenAI SDK 适配器 (239 行)
│   └── provider_test.go
└── anthropic/
    ├── provider.go  # Anthropic SDK 适配器 (287 行)
    └── provider_test.go

cmd/goforge/main.go  # CLI 交互式 Chat Demo (94 行)
```

**总计**：~500 行核心代码 + ~350 行测试 = ~850 行

---

## 2. 核心接口：`llm.LLM`

```go
type LLM interface {
    Chat(ctx context.Context, messages []Message, opts ...Option) (*Response, error)
    ChatStream(ctx context.Context, messages []Message, opts ...Option) iter.Seq2[Chunk, error]
}
```

### 2.1 框架对照

| 框架 | 接口名 | Chat 方法签名 | Stream 方法签名 |
|------|--------|-------------|----------------|
| **GoForge** | `LLM` | `Chat(ctx, []Message, ...Option) (*Response, error)` | `ChatStream(ctx, []Message, ...Option) iter.Seq2[Chunk, error]` |
| **Eino** | `ChatModel` | `Generate(ctx, []*Message, ...Option) (*Message, error)` | `Stream(ctx, []*Message, ...Option) (*StreamReader[*Message], error)` |
| **tRPC-Agent-Go** | `Model` | `GenerateContent(ctx, *Request) (<-chan *Response, error)` | 同左（channel 统一承载流/非流） |
| **ADK-Go** | `Model` | `GenerateContent(ctx, *GenerateContentConfig) (*GenerateContentResponse, error)` | `GenerateContentStream(ctx, *GenerateContentConfig) GenerateContentResponseIterator` |

**GoForge 的设计选择**：

1. **`iter.Seq2` 而非 channel / StreamReader**：Go 1.23 引入的 range-over-func，调用方用 `for chunk, err := range stream` 消费，比 channel 更简洁（无需手动 close），比 StreamReader 更符合 Go 惯用法。
2. **两个方法而非统一方法**：tRPC-Agent-Go 用一个 `GenerateContent` 方法 + `Request.Stream` 字段同时承载流/非流。GoForge 选择显式分离，调用方不需要判断 channel 是否流式。
3. **接口窄度 = 2 个方法**：遵循 AGENTS.md 的 "1-3 methods" 原则。Eino 的 `ChatModel` 有 3 个方法（`Generate`、`Stream`、`BindTools`），ADK-Go 的 `Model` 更宽。

### 2.2 设计决策表

| 决策 | GoForge | 对照框架 | 理由 |
|------|---------|---------|------|
| Stream 返回类型 | `iter.Seq2[Chunk, error]` | Eino: `*StreamReader`; tRPC: `<-chan *Response`; ADK: `Iterator` | Go 1.23 原生语义，for-range 自然消费 |
| 方法拆分 | `Chat` + `ChatStream` | tRPC: 统一 `GenerateContent` | 调用方意图明确，无需检查 `Request.Stream` |
| Options 传递 | `...Option` 变参 | Eino: `...Option`; tRPC: `*Request` 结构体 | 函数式选项可扩展而不破坏签名 |
| 错误处理 | `(result, error)` | Eino 同; tRPC: channel 内嵌 error; ADK: iterator 的 `Err()` | Go 标准双返回值 |

---

## 3. 消息类型设计

### 3.1 GoForge 的提供商无关类型

```go
type Message struct {
    Role       Role
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string
}

type ToolCall struct {
    ID   string
    Name string
    Args string  // raw JSON
}
```

### 3.2 框架对照

| 框架 | 消息类型 | 绑定程度 | ToolCall.Args 类型 |
|------|---------|---------|-------------------|
| **GoForge** | `llm.Message` | 完全解耦 | `string`（JSON 字符串） |
| **Eino** | `schema.Message` | 自定义（解耦） | `string`（JSON 字符串） |
| **tRPC-Agent-Go** | `model.Message` | 自定义（解耦） | `string`（JSON 字符串） |
| **ADK-Go** | `genai.Content` / `genai.Part` | **绑定 Google genai SDK** | `map[string]any` |

**关键对比**：

- GoForge、Eino、tRPC-Agent-Go 都使用自定义消息类型，在 provider 边界做双向转换。
- ADK-Go 直接使用 `genai.Content`，如果要换非 Google 模型，上层代码全部要改。这是我们明确避免的反模式。
- `ToolCall.Args` 保持 `string`（JSON）而非 `map[string]any`，原因：避免反序列化后再序列化的损耗，保持 JSON 精度（数字不会被 `float64` 截断）。

### 3.3 角色映射

| GoForge Role | OpenAI | Anthropic |
|-------------|--------|-----------|
| `system` | `system` | 提取到 `MessageNewParams.System`（不在 messages 中） |
| `user` | `user` | `user` |
| `assistant` | `assistant` | `assistant` |
| `tool` | `tool`（含 `tool_call_id`） | 映射为 `user` + `tool_result` content block |

Anthropic 的角色映射最特殊：system prompt 独立、tool result 属于 user 消息。GoForge 在 `convertMessages` 中处理这些差异，上层代码完全无感。

---

## 4. Provider 架构：薄适配器模式

### 4.1 架构图

```
┌────────────────────────────────────┐
│           调用方代码                │
│   p.Chat(ctx, []llm.Message{...})  │
└──────────────┬─────────────────────┘
               │
    ┌──────────▼──────────┐
    │     llm.LLM 接口    │   ← Ring 边界
    └──────────┬──────────┘
               │
  ┌────────────┼────────────┐
  │            │            │
  ▼            ▼            ▼
┌──────┐  ┌──────────┐  ┌────────┐
│OpenAI│  │Anthropic │  │  ...   │  ← 薄适配器
│  SDK │  │   SDK    │  │        │
└──┬───┘  └────┬─────┘  └────────┘
   │           │
   ▼           ▼
 HTTP        HTTP          ← SDK 内部处理
```

每个 Provider 只做三件事：
1. **outbound**: `llm.Message` → SDK 请求类型
2. **call**: 调用 SDK 的 `New()` / `NewStreaming()`
3. **inbound**: SDK 响应类型 → `llm.Response` / `llm.Chunk`

### 4.2 所有框架的 Provider 都用官方 SDK

| 框架 | OpenAI SDK | Anthropic SDK |
|------|-----------|--------------|
| **GoForge** | `openai/openai-go v1.12.0` | `anthropics/anthropic-sdk-go v1.48.0` |
| **Eino** | `sashabaranov/go-openai` | `anthropics/anthropic-sdk-go` |
| **tRPC-Agent-Go** | `openai/openai-go` | `anthropics/anthropic-sdk-go` |
| **ADK-Go** | `google.golang.org/genai`（仅 Google） | 不支持 |

> **结论**：业界共识——Provider 层使用官方/社区 SDK，不手写 HTTP 和 SSE 解析。

### 4.3 代码对照：OpenAI Provider

#### 4.3.1 构造函数

**GoForge**:
```go
func New(cfg Config) *Provider {
    var opts []option.RequestOption
    if cfg.APIKey != "" {
        opts = append(opts, option.WithAPIKey(cfg.APIKey))
    }
    if cfg.BaseURL != "" {
        opts = append(opts, option.WithBaseURL(cfg.BaseURL))
    }
    return &Provider{
        client: sdkopenai.NewClient(opts...),
        model:  cfg.Model,
    }
}
```

**tRPC-Agent-Go**:
```go
func New(name string, opts ...Option) *Model {
    var clientOpts []openaiopt.RequestOption
    if o.APIKey != "" {
        clientOpts = append(clientOpts, openaiopt.WithAPIKey(o.APIKey))
    }
    if o.BaseURL != "" {
        clientOpts = append(clientOpts, openaiopt.WithBaseURL(o.BaseURL))
    }
    clientOpts = append(clientOpts, openaiopt.WithHTTPClient(model.DefaultNewHTTPClient(...)))
    client := openai.NewClient(clientOpts...)
    // ...
}
```

**差异**：GoForge 简化版（Config 结构体替代 Option 链）；tRPC 还注入了自定义 HTTP client、回调钩子等生产特性。

#### 4.3.2 消息转换

**GoForge** — 用 SDK 的便捷构造器：
```go
func convertMessages(msgs []llm.Message) []sdkopenai.ChatCompletionMessageParamUnion {
    for i, m := range msgs {
        switch m.Role {
        case llm.RoleSystem:
            out[i] = sdkopenai.SystemMessage(m.Content)
        case llm.RoleUser:
            out[i] = sdkopenai.UserMessage(m.Content)
        case llm.RoleAssistant:
            // 填充 OfAssistant union 字段 + ToolCalls
        case llm.RoleTool:
            out[i] = sdkopenai.ToolMessage(m.ToolCallID, m.Content)
        }
    }
}
```

**tRPC-Agent-Go** — 同样按 role 分支，使用 union 的 `Of*` 字段：
```go
switch msg.Role {
case model.RoleSystem:
    result[i] = openai.ChatCompletionMessageParamUnion{
        OfSystem: &openai.ChatCompletionSystemMessageParam{...},
    }
// ...
}
```

**差异**：本质一致。GoForge 用 `SystemMessage()` 快捷构造，tRPC 直接填 union 字段。

#### 4.3.3 流式处理

**GoForge**:
```go
func (p *Provider) ChatStream(...) iter.Seq2[llm.Chunk, error] {
    return func(yield func(llm.Chunk, error) bool) {
        stream := p.client.Chat.Completions.NewStreaming(ctx, params)
        defer stream.Close()
        for stream.Next() {
            chunk := convertStreamChunk(stream.Current())
            if !yield(chunk, nil) { return }
        }
        if err := stream.Err(); err != nil {
            yield(llm.Chunk{}, err)
        }
    }
}
```

**tRPC-Agent-Go**:
```go
func (m *Model) handleStreamingResponseWithEmitter(...) {
    stream := m.client.Chat.Completions.NewStreaming(ctx, chatRequest, opts...)
    defer stream.Close()
    acc := openai.ChatCompletionAccumulator{}
    for stream.Next() {
        chunk := stream.Current()
        acc.AddChunk(chunk)
        emit(createPartialResponse(chunk))
    }
    emitFinal(acc)
}
```

**差异**：
- GoForge 用 `iter.Seq2` 直接 yield，tRPC 用 `channel + goroutine` 发射。
- tRPC 使用 SDK 内置 `ChatCompletionAccumulator` 做最终聚合，GoForge 在 M1 阶段不需要（留给 M3 Agent 层处理）。
- tRPC 有大量 defensive 处理（`sanitizeChunkForAccumulator`、`stripReasoningFromChunkForAccumulator`），这些是生产环境中遇到的边界情况，GoForge 保持简洁。

### 4.4 代码对照：Anthropic Provider

#### 4.4.1 消息转换的差异

**GoForge** — system prompt 提取为 `TextBlockParam`：
```go
func convertMessages(msgs []llm.Message) ([]sdkanthropic.MessageParam, []sdkanthropic.TextBlockParam) {
    for _, m := range msgs {
        switch m.Role {
        case llm.RoleSystem:
            systemBlocks = append(systemBlocks, sdkanthropic.TextBlockParam{Text: m.Content})
        case llm.RoleTool:
            apiMessages = append(apiMessages, sdkanthropic.NewUserMessage(
                sdkanthropic.NewToolResultBlock(m.ToolCallID, m.Content, false),
            ))
        // ...
        }
    }
}
```

**tRPC-Agent-Go** — 类似逻辑，还处理连续 tool_result 合并：
```go
func convertMessages(messages []model.Message) (msgs, systemPrompts, error) {
    // system → systemPrompts
    // tool → convertToolResult, merge consecutive tool results into one user message
}
```

**差异**：
- tRPC 处理连续 tool_result 合并（Anthropic 要求 user/assistant 交替），GoForge 暂不需要（M2 Tool Calling 阶段会加）。
- 两者都把 system prompt 独立提取，这是 Anthropic API 的要求。

#### 4.4.2 流式事件处理

**GoForge** — 直接用 union 的 Type 字段分支：
```go
func convertStreamEvent(event sdkanthropic.MessageStreamEventUnion) (llm.Chunk, bool) {
    switch event.Type {
    case "content_block_start":
        // tool_use 开始
    case "content_block_delta":
        switch event.Delta.Type {
        case "text_delta": ...
        case "input_json_delta": ...
        }
    case "message_delta":
        delta := event.AsMessageDelta()
        // stop_reason + usage
    }
}
```

**tRPC-Agent-Go** — 用 `AsAny()` 做类型断言：
```go
switch event := event.AsAny().(type) {
case anthropic.MessageStartEvent:      ...
case anthropic.MessageDeltaEvent:      ...
case anthropic.ContentBlockStartEvent: ...
case anthropic.ContentBlockDeltaEvent: ...
}
```

**差异**：GoForge 用 `event.Type` 字符串匹配 + 字段直取，tRPC 用 `AsAny()` 类型断言。前者更直观，后者类型安全更强。两种写法等价，GoForge 选择可读性。

---

## 5. 函数式选项

### 5.1 GoForge 实现

```go
type Options struct {
    Model         string
    Temperature   *float64
    MaxTokens     int
    TopP          *float64
    StopSequences []string
    Tools         []ToolSchema
}

type Option func(*Options)

func WithTemperature(t float64) Option {
    return func(o *Options) { o.Temperature = &t }
}
```

### 5.2 框架对照

| 框架 | 选项机制 | 传递方式 |
|------|---------|---------|
| **GoForge** | `func(*Options)` 函数式 | `...Option` 变参 |
| **Eino** | `func(*Options)` 函数式 | `...Option` 变参 |
| **tRPC-Agent-Go** | `*Request` 结构体 | 单一参数 |
| **ADK-Go** | `*GenerateContentConfig` 结构体 | 单一参数 |

GoForge 和 Eino 采用同一模式，好处是向后兼容——新增选项不修改调用方签名。tRPC/ADK 的结构体方式更直接但每次新增字段理论上是 breaking change（实践中 Go 的零值语义缓解了这个问题）。

---

## 6. 测试策略

### 6.1 方法

| 测试类型 | 方法 | 覆盖范围 |
|---------|------|---------|
| **Provider 集成** | `httptest.NewServer` 返回标准 JSON | Chat、ChatStream、API 错误 |
| **纯函数单元** | 表驱动测试 | `convertFinishReason`、`convertStopReason` |
| **消息类型** | 构造器验证 | `SystemMessage`、`UserMessage` 等 |
| **选项** | 应用 + 覆盖 | `ApplyOptions` 默认值、组合、覆盖 |

### 6.2 与 tRPC-Agent-Go 对比

tRPC 的测试更侧重回调链和复杂的边界情况（tool call index 修复、reasoning content 提取），因为它需要兼容众多 OpenAI-API-compatible 提供商的差异行为。GoForge M1 聚焦核心路径的正确性。

---

## 7. 重构演进

### 7.1 从手写 HTTP 到 SDK 适配器

M1 经历了一次关键重构：

| 阶段 | 实现方式 | 代码量 | 问题 |
|------|---------|--------|------|
| **V1** | 手写 `net/http` + 自定义 SSE 解析 | ~600 行 | 需维护 wire types、SSE 解析、错误处理 |
| **V2** | 官方 SDK 薄适配器 | ~526 行 | SDK 处理 HTTP/SSE/重试/类型 |

**重构收益**：
- 删除 `types.go`（OpenAI 91 行 + Anthropic 91 行 = 182 行）
- 删除 `internal/httputil/sse.go`（46 行）
- 错误处理由 SDK 标准化（不再手动解析 HTTP 状态码）
- SSE 流解析由 SDK 内部处理（包括 keep-alive、异常事件等边界情况）

### 7.2 为什么一开始要手写？

这是一个有意的学习过程：
1. **理解底层**：手写 HTTP 请求和 SSE 解析后，才真正理解 SDK "背后做了什么"
2. **识别共性**：OpenAI 和 Anthropic 的 SSE 格式高度相似，促使我们提取 `internal/httputil/sse.go`
3. **认知升级**：手写后再看 tRPC-Agent-Go 和 Eino 的代码，能准确理解它们为什么这样设计

> **教训**：Provider 层不是展示 HTTP 技能的地方。用 SDK 把精力省出来，投入到 Ring 2-4 的原创设计上。

---

## 8. 当前未覆盖（留给后续 Milestone）

| 能力 | 当前状态 | 计划 |
|------|---------|------|
| Retry & Rate Limiting | 未实现 | M5（Pipeline 可靠性） |
| 多模态消息（图片/音频） | 仅文本 | M7 扩展 |
| Reasoning / Thinking | 未映射 | M3（Agent 需要） |
| 自定义 HTTP Client | SDK 默认 | 需要时通过 SDK option 注入 |
| Token 计数 | 依赖 API Usage | M4（Context Engineering） |
| 连续 tool_result 合并 | 未处理 | M2（Tool Calling） |

---

## 9. 总结：GoForge M1 与框架的位置

```
                 复杂度
                   ↑
tRPC-Agent-Go ────┤  生产级：回调链、provider 兼容层、自定义 decoder
                   │
Eino ─────────────┤  平台级：Graph 编译、StreamReader、中间件
                   │
GoForge M1 ───────┤  学习级：核心路径、SDK 适配、iter.Seq2
                   │
ADK-Go ───────────┤  SDK 绑定：genai.Content、Google-only
                   ↓
                 抽象度
```

GoForge M1 处于 "学习级" 复杂度，但在接口设计上与 Eino/tRPC 对齐：
- 自定义消息类型（不绑定 SDK）
- 薄适配器 Provider
- `iter.Seq2` 流式（Go 1.23 最新实践）
- 函数式选项

后续 M2-M7 会逐步补齐 Tool、Agent、Pipeline 层，最终达到 "生产级" 复杂度。
