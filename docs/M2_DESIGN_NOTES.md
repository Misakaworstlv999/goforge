# M2: Tool Calling — 设计笔记与框架对照

本文档详细解释 GoForge M2 阶段的代码架构，并与 Eino (ByteDance)、tRPC-Agent-Go (Tencent) 两个参考框架进行逐层对照。

---

## 1. 整体结构

M2 阶段完成了 Ring 2（Tool System）的核心实现，包含以下文件：

```
pkg/tool/
├── tool.go           # Tool 接口 + NewTool[Args] 泛型构造器 (62 行)
├── tool_test.go      # 7 个测试：接口/schema/执行/错误 (181 行)
├── registry.go       # 并发安全 Registry (89 行)
├── registry_test.go  # 7 个测试：注册/重名/排序/执行/并发 (153 行)
├── executor.go       # ExecuteAll 批量执行 (19 行)
├── executor_test.go  # 3 个测试：全成功/部分失败/空 (72 行)
└── builtin/
    ├── calc.go       # Calculator 工具 (46 行)
    ├── calc_test.go  # 9 个子测试 (60 行)
    ├── clock.go      # Clock 工具 (33 行)
    └── clock_test.go # 4 个测试 (55 行)

cmd/goforge/main.go   # CLI 更新：-tools 模式演示单步 tool calling (163 行)
```

**总计**：~250 行核心代码 + ~520 行测试 = ~770 行

---

## 2. 核心接口：`tool.Tool`

```go
type Tool interface {
    Name() string
    Description() string
    Schema() llm.ToolSchema
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

### 2.1 框架对照

| 框架 | 接口名 | 方法数 | Execute 参数 | Execute 返回值 |
|------|--------|--------|-------------|---------------|
| **GoForge** | `Tool` | 4 | `json.RawMessage` | `(string, error)` |
| **Eino** | `InvokableTool` (嵌入 `BaseTool`) | 2 | `string`（JSON 字符串） | `(string, error)` |
| **tRPC-Agent-Go** | `CallableTool` (嵌入 `Tool`) | 2 | `[]byte`（JSON） | `(any, error)` |

### 2.2 设计决策表

| 决策 | GoForge | 对照框架 | 理由 |
|------|---------|---------|------|
| 接口拆分 | 单一 `Tool` 接口，4 个方法 | Eino: `BaseTool` + `InvokableTool` + `StreamableTool`；tRPC: `Tool` + `CallableTool` + `StreamableTool` | M2 不需要流式工具执行，保持简单 |
| Execute 参数类型 | `json.RawMessage` | Eino: `string`；tRPC: `[]byte` | 本质一致——都是延迟反序列化的 JSON，`json.RawMessage` 语义更明确（标准库类型） |
| Execute 返回值 | `(string, error)` | Eino: `(string, error)`；tRPC: `(any, error)` | 与 Eino 一致。返回 `string` 因为 LLM 消息的 content 就是字符串，无需额外序列化步骤 |
| Schema 返回 | 直接返回 `llm.ToolSchema` | Eino: `*schema.ToolInfo`；tRPC: `*Declaration` | 复用 M1 已有类型，零新类型引入 |
| 流式工具 | 不支持（M2 范围外） | Eino: `StreamableTool`；tRPC: `StreamableTool` | 留给 M5+ 需要时再扩展 |

### 2.3 Eino 的多层接口 vs GoForge 的统一接口

Eino 将工具拆为三层：

```go
// Eino 的设计
type BaseTool interface {
    Info(ctx context.Context) (*schema.ToolInfo, error)  // 仅 schema
}
type InvokableTool interface {
    BaseTool
    InvokableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (string, error)
}
type StreamableTool interface {
    BaseTool
    StreamableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (*StreamReader[string], error)
}
```

这种设计允许"只提供 schema 但不可执行"的场景（如纯 schema 传递给 ChatModel），但增加了类型系统的复杂度。

GoForge 选择单一 `Tool` 接口的原因：**在 GoForge 的架构中，工具总是可执行的**。schema-only 的场景通过 `Registry.Schemas()` 方法处理——它返回 `[]llm.ToolSchema`，不需要 `Tool` 实例。

### 2.4 tRPC 的 `(any, error)` 返回 vs GoForge 的 `(string, error)` 返回

tRPC 的 `Call` 返回 `any`：

```go
// tRPC-Agent-Go
func (ft *FunctionTool[I, O]) Call(ctx context.Context, jsonArgs []byte) (any, error)
```

这允许工具返回结构体，由框架负责序列化。但实际上 Agent 最终要把结果作为字符串放入 tool message，所以 tRPC 内部还是会 `json.Marshal(result)`。

GoForge 直接返回 `string`，省去一层间接——**工具函数自己决定如何格式化输出**。这与 Eino 的 `(string, error)` 一致。

---

## 3. 泛型构造器：`NewTool[Args]`

### 3.1 GoForge 实现

```go
func NewTool[Args any](name, desc string, fn func(context.Context, Args) (string, error)) Tool
```

内部流程：
1. `jsonschema.Reflector{DoNotReference: true}.Reflect(new(Args))` → `*jsonschema.Schema`
2. 清除 `$schema` 和 `$id`（LLM 不需要文档级元数据）
3. 构造 `funcTool[Args]`，`Execute` 时做 `json.Unmarshal` → `fn(ctx, typedArgs)`

### 3.2 框架对照

| 框架 | 泛型构造器 | 类型参数 | Schema 生成 |
|------|-----------|---------|------------|
| **GoForge** | `NewTool[Args]` | 1 个：`Args` | `invopop/jsonschema` 反射 |
| **Eino** | `InferTool[T, D]` / `NewTool[T, D]` | 2 个：`T`(入参) + `D`(出参) | 自有 `goStruct2ToolInfo` 反射 |
| **tRPC-Agent-Go** | `NewFunctionTool[I, O]` | 2 个：`I`(入参) + `O`(出参) | 自有 `GenerateJSONSchema` 反射 |

### 3.3 为什么 GoForge 只有 1 个类型参数？

Eino 和 tRPC 都用 2 个类型参数（入参 + 出参），因为它们需要为 output 也生成 schema（用于 Agent 间通信或类型安全的 pipeline）。

GoForge 的 `NewTool[Args]` 只有 1 个类型参数（入参），因为：
- **出参始终是 `string`**：LLM tool message 的 content 就是字符串
- **减少概念负担**：1 个类型参数 vs 2 个，调用更简洁
- **后续可扩展**：如果 M5+ 需要结构化出参，可以新增 `NewTypedTool[I, O]` 而不影响现有 API

### 3.4 Schema 生成对比

**GoForge** — 使用第三方库 `invopop/jsonschema`：

```go
r := &jsonschema.Reflector{DoNotReference: true}
schema := r.Reflect(new(Args))
schema.Version = ""  // 清除 $schema
schema.ID = ""       // 清除 $id
```

**tRPC-Agent-Go** — 自研 `GenerateJSONSchema`：

```go
// internal/tool/schema.go
func GenerateJSONSchema(t reflect.Type) *Schema {
    // 手写反射，遍历 struct fields，解析 json/jsonschema tags
    // 支持嵌套、数组、enum、required 等
}
```

**Eino** — 自研 `goStruct2ToolInfo`：

```go
func goStruct2ToolInfo[T any](name, desc string, opts ...Option) (*schema.ToolInfo, error) {
    // 类似 tRPC，手写反射生成 schema
}
```

**GoForge 的选择理由**：

| 方案 | 代码量 | 维护成本 | 功能完整度 |
|------|--------|---------|-----------|
| `invopop/jsonschema` | ~5 行调用 | 零（三方维护） | 完整 JSON Schema draft-2020-12 |
| 自研反射 | ~200-300 行 | 需自行处理边界情况 | 仅覆盖常用子集 |

`invopop/jsonschema` 已是间接依赖（`anthropic-sdk-go` 引入），零新增依赖。tRPC 自研是因为他们需要精确控制 schema 生成以兼容多种 LLM API 的差异格式要求，GoForge 不需要这个级别的控制。

### 3.5 `DoNotReference: true` 的作用

默认情况下 `jsonschema.Reflector` 会为嵌套结构体生成 `$ref` 引用并放入 `$defs`：

```json
// 默认（有 $ref）
{
  "properties": {
    "filter": { "$ref": "#/$defs/FilterSpec" }
  },
  "$defs": {
    "FilterSpec": { "type": "object", ... }
  }
}
```

大多数 LLM API **不支持** JSON Schema 的 `$ref` / `$defs`，它们期望内联的完整定义。`DoNotReference: true` 确保所有嵌套类型直接展开：

```json
// DoNotReference: true（内联）
{
  "properties": {
    "filter": {
      "type": "object",
      "properties": { "limit": { "type": "integer" }, ... }
    }
  }
}
```

---

## 4. Tool Registry

### 4.1 GoForge 实现

```go
type Registry struct {
    mu    sync.RWMutex
    tools map[string]Tool
}

func (r *Registry) Register(tools ...Tool) error   // 重名报错
func (r *Registry) Get(name string) (Tool, bool)
func (r *Registry) Schemas() []llm.ToolSchema      // 按名称排序
func (r *Registry) Execute(ctx, call) (ToolResult, error)
```

### 4.2 框架对照

| 框架 | 注册机制 | 并发安全 | Schema 导出 | 排序 |
|------|---------|---------|------------|------|
| **GoForge** | `Registry.Register(tools...)` | `sync.RWMutex` | `Schemas() []ToolSchema` | 按名称排序 |
| **Eino** | 无独立 Registry，ToolsNode 持有 `[]Tool` | 不适用（构造时确定） | `ToolsNode` 内部转换 | 无 |
| **tRPC-Agent-Go** | `ToolSet` 接口 + 多种实现 | 取决于实现 | `Tools() []Declaration` | 无 |

### 4.3 为什么 Schemas 排序？

```go
sort.Slice(schemas, func(i, j int) bool {
    return schemas[i].Name < schemas[j].Name
})
```

LLM API 的 prompt cache（如 Anthropic 的 prompt caching）是基于请求内容的哈希。如果每次请求的 tools 数组顺序不同，cache 命中率下降。**按名称排序保证确定性顺序**，这个实践来自 tRPC-Agent-Go 的源码注释。

### 4.4 GoForge 的 Registry vs tRPC 的 ToolSet

tRPC 的 `ToolSet` 是一个接口：

```go
// tRPC-Agent-Go
type ToolSet interface {
    Tools() []*Declaration
    Call(ctx, name, jsonArgs) (any, error)
}
```

这允许动态工具集（如 MCP 远程工具、OpenAPI 自动生成工具等），工具列表可以运行时变化。

GoForge 的 `Registry` 是一个具体类型，功能等价但更简单。M2 不需要动态工具集（MCP 支持计划在 M6），现阶段用具体类型即可，后续可以提取接口。

---

## 5. ExecuteAll：批量执行

### 5.1 实现

```go
func ExecuteAll(ctx context.Context, reg *Registry, calls []llm.ToolCall) []llm.ToolResult {
    results := make([]llm.ToolResult, 0, len(calls))
    for _, call := range calls {
        result, _ := reg.Execute(ctx, call)
        results = append(results, result)
    }
    return results
}
```

### 5.2 设计要点

1. **顺序执行**：M2 范围，M3 升级为 `errgroup` 并行
2. **容错**：单个工具失败不中断其他。错误通过 `ToolResult.IsError` 传播给 LLM
3. **忽略 error 返回值**：`Registry.Execute` 的 error 已经被编码进 `ToolResult.IsError` 和 `ToolResult.Content`，无需重复处理

### 5.3 为什么 M2 不直接做并行？

并行执行涉及多个额外设计决策：
- 最大并发度（goroutine 上限）
- 超时控制（单个工具超时 vs 整体超时）
- Context 取消传播
- 错误策略（fail-fast vs best-effort）

这些与 Agent Runtime（Ring 3）的 ReAct 循环紧密耦合。M2 的职责是验证工具系统的核心链路，并行是优化手段。

---

## 6. 内置工具

### 6.1 Calculator

```go
type CalcArgs struct {
    A  float64 `json:"a" jsonschema:"description=First number,required"`
    B  float64 `json:"b" jsonschema:"description=Second number,required"`
    Op string  `json:"op" jsonschema:"description=Operation,enum=add,enum=subtract,enum=multiply,enum=divide,required"`
}

func NewCalculator() tool.Tool {
    return tool.NewTool[CalcArgs]("calculator", "Basic arithmetic operations", calcFn)
}
```

### 6.2 Clock

```go
type ClockArgs struct {
    Timezone string `json:"timezone,omitempty" jsonschema:"description=IANA timezone name (e.g. America/New_York). Defaults to UTC."`
}

func NewClock() tool.Tool {
    return tool.NewTool[ClockArgs]("current_time", "Get current date and time", clockFn)
}
```

### 6.3 与框架的内置工具规模对比

| 框架 | 内置工具数 | 典型工具 | 定位 |
|------|-----------|---------|------|
| **GoForge M2** | 2 | calculator, clock | 验证性：证明 Tool System 链路完整 |
| **Eino** | 0 | 无 | 纯框架：不提供任何内置工具 |
| **tRPC-Agent-Go** | 21 个包 | file、hostexec、codeexec、webfetch、mcp、todo 等 | 生产级 Coding Agent 全套 |

GoForge 走的是 Eino 的路线——框架提供抽象，工具按需扩展。calculator 和 clock 存在的意义是**端到端验证**泛型构造器、schema 生成、Registry、ExecuteAll 的完整链路，而非实际生产能力。实际有用的工具（shell_exec、file_ops）依赖 M3 的 Agent Loop 才有意义。

### 6.4 struct tag 约定

```go
`json:"a" jsonschema:"description=First number,required"`
`json:"op" jsonschema:"description=Operation,enum=add,enum=subtract,enum=multiply,enum=divide,required"`
`json:"timezone,omitempty" jsonschema:"description=IANA timezone name"`
```

这套约定与 Eino 一致：
- `json:"field_name"` — JSON 字段名
- `jsonschema:"description=..."` — LLM 看到的字段说明
- `jsonschema:"enum=a,enum=b"` — 枚举值约束
- `jsonschema:"required"` — 标记必填
- `omitempty` — 可选字段

---

## 7. M1→M2 衔接：类型复用

M2 没有引入新的 LLM 层类型，完全复用 M1 已有定义：

```
M1 定义                      M2 使用位置
─────────────────────────────────────────────────────
llm.ToolSchema               Tool.Schema() 返回值
llm.ToolCall                  Registry.Execute() 入参
llm.ToolResult                Registry.Execute() 返回值
llm.WithTools()               CLI demo 传入 schemas
llm.ToolMessage()             CLI demo 构造 tool result message
llm.StopReasonToolCall        CLI demo 判断是否需要执行工具
```

这验证了 M1 的类型设计是前瞻性的——Ring 1 预留的 Tool 类型在 Ring 2 无需修改即可使用。

---

## 8. CLI 集成：单步 Tool Calling 演示

### 8.1 流程

```
用户输入 → Chat(messages, WithTools(schemas))
                    │
            ┌───────┴────────┐
            │                │
    StopReason=end     StopReason=tool_call
    直接输出回答         │
                        ▼
                  ExecuteAll(calls)
                        │
                        ▼
                  Chat(messages + tool results)
                        │
                        ▼
                    输出最终回答
```

### 8.2 单步 vs 循环

M2 的 `handleToolChat` 只做**一轮** tool call：LLM 请求工具 → 执行 → 再次 Chat。

如果第二次 Chat 又返回 tool_call，当前实现会直接输出空内容。这是有意为之——**多轮循环是 M3 ReAct Agent 的职责**。M2 的目标是验证单步 tool calling 的完整链路。

### 8.3 实际运行示例

```bash
$ go run cmd/goforge/main.go -tools -base-url "https://api.example.com" -api-key "sk-xxx"
GoForge Chat — Tool Calling Mode (type 'exit' to quit)
Available tools: calculator, current_time
---

> 3.14159 的平方是多少？
[Calling 1 tool(s)...]
  → calculator({"a":3.14159,"b":3.14159,"op":"multiply"})
  ✓ call_abc123: 9.869587728099999
3.14159 的平方约等于 9.8696。
```

---

## 9. 踩坑记录

### 9.1 SDK ToolMessage 参数顺序

**问题**：`openai-go` v1.12.0 的 `ToolMessage` 签名是 `ToolMessage(content, toolCallID)`，而不是直觉中的 `ToolMessage(toolCallID, content)`。

```go
// openai-go SDK 实际签名
func ToolMessage[T string | ...](content T, toolCallID string) ChatCompletionMessageParamUnion

// 我们最初写的（错误）
out[i] = sdkopenai.ToolMessage(m.ToolCallID, m.Content)
// 传给 API: tool_call_id = "9.869604401089358"（实际上是 content）

// 修复后（正确）
out[i] = sdkopenai.ToolMessage(m.Content, m.ToolCallID)
```

**教训**：SDK 的函数参数顺序不一定符合直觉。`go doc` 是最可靠的验证手段。这个 bug 在本地测试不会暴露（mock server 不校验 tool_call_id），只有接入真实 API 才会触发 400 错误。

### 9.2 jsonschema 的 `$ref` 问题

默认的 `jsonschema.Reflector` 会为嵌套结构体生成 `$ref` 引用，大多数 LLM API 不支持。必须设置 `DoNotReference: true`。

### 9.3 jsonschema 的文档级元数据

`Reflector.Reflect()` 会添加 `$schema` 和 `$id` 顶级字段。LLM tool schema 需要的是纯粹的对象定义，必须手动清除这些字段。

---

## 10. 当前未覆盖（留给后续 Milestone）

| 能力 | 当前状态 | 计划 |
|------|---------|------|
| 并行 tool execution | 顺序执行 | M3（errgroup） |
| Agent Loop（ReAct） | 单步 tool call | M3 |
| 系统工具（shell/file） | 仅 calc + clock | M3-M4 |
| 工具执行超时 | 无 | M3（context deadline） |
| 动态工具集（ToolSet 接口） | 具体 Registry 类型 | M6（MCP） |
| 流式工具执行 | 不支持 | M5+ |
| 工具权限控制 | 无 | M5（Pipeline 安全） |

---

## 11. 总结：GoForge M2 与框架的位置

```
                    工具系统复杂度
                          ↑
tRPC-Agent-Go ───────────┤  21 个工具包 + ToolSet 接口 + MCP + OpenAPI 自动工具
                          │  自研 Schema 生成 + StreamableTool
                          │
Eino ────────────────────┤  BaseTool/InvokableTool/StreamableTool 三层
                          │  自研 Schema 生成 + Enhanced 多模态工具
                          │
GoForge M2 ──────────────┤  单一 Tool 接口 + NewTool[Args] 泛型构造器
                          │  invopop/jsonschema + Registry + ExecuteAll
                          │  2 个验证性内置工具
                          ↓
                    接口简洁度
```

M2 在接口简洁度上做到了极致：
- **1 个工具接口**（vs Eino 的 5 个、tRPC 的 3 个）
- **1 个类型参数**（vs Eino/tRPC 的 2 个）
- **0 个新 LLM 层类型**（完全复用 M1）
- **~250 行核心代码** 实现完整 Tool System

这种简洁不是偷工减料——而是在 M2 的验证性目标下做出的有意取舍。复杂度预算留给了 M3（Agent Runtime）和 M4（Pipeline Engine），那里才是 GoForge 核心创新（Stage-Aware Context Loading、Verification-Gated Pipeline）的战场。
