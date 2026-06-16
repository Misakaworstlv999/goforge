# M3: ReAct Agent — 设计笔记与框架对照

本文档详细解释 GoForge M3 阶段（Ring 3 — Agent Runtime）的代码架构，并与 Eino (ByteDance)、tRPC-Agent-Go (Tencent)、ADK-Go (Google) 三个参考框架逐层对照。

M3 的核心命题：**用一段手写的、无图抽象的 ReAct 循环（~85 行），把 M1 的 LLM 客户端和 M2 的工具系统串成一个能多轮 think-act-observe 的 Agent。**

---

## 1. 整体结构

M3 完成了 Ring 3 的核心（M3-001~004），新增/修改以下文件：

```
pkg/agent/
├── event.go          # Event sum type（枚举 + tagged struct）+ 5 个构造器 (81 行)
├── event_test.go     # String()/构造器字段表驱动测试 (83 行)
├── agent.go          # Agent 接口 + SimpleAgent + Option + ReAct 循环 (147 行)
└── agent_test.go     # mock LLM 驱动的循环/maxSteps/错误/break 测试 (202 行)

pkg/tool/
├── executor.go       # 新增 ExecuteParallel（errgroup + per-tool 超时）；保留 ExecuteAll (51 行)
└── executor_test.go  # 追加并行测试：顺序保留/失败隔离/超时 (180 行)

internal/cli/         # CLI 以 -mode agent 接入（Ring 5 边缘层，另见独立重构）
```

**核心代码**（event.go + agent.go 非空行）：~195 行，其中 ReAct 循环本体 ~85 行。
依赖新增：`golang.org/x/sync`（errgroup）从间接提升为直接依赖。

---

## 2. Event 类型：`agent.Event`

```go
type EventType int
const (
    EventThink      EventType = iota // LLM 推理文本
    EventToolCall                    // 单个工具调用请求
    EventToolResult                  // 单个工具执行结果
    EventResponse                    // 最终答复
    EventError                       // 终止性错误
)

type Event struct {
    Type       EventType
    Content    string
    ToolCall   *llm.ToolCall    // EventToolCall 时非空
    ToolResult *llm.ToolResult  // EventToolResult 时非空
    Usage      *llm.Usage       // EventThink/EventResponse 时携带
    Step       int              // ReAct 步数（调试用）
}
```

### 2.1 框架对照

| 框架 | 事件模型 | 形态 | 流式表达 |
|------|---------|------|---------|
| **GoForge** | `Event{Type, Content, ToolCall, ToolResult, Usage, Step}` | 枚举 + tagged struct（sum type） | 不在事件里区分流式，逐事件 yield |
| **Eino** | `TypedAgentEvent[M]{Output, Action, Err, ...}` | 泛型 tagged struct | `IsStreaming` 标志 + `MessageStream` 惰性流 |
| **ADK-Go** | `session.Event`（内嵌 `model.LLMResponse`） | 富元数据 struct | 深度内嵌进 session 模型 |
| **tRPC-Agent-Go** | `ExecutionPhase*` 阶段事件 | 阶段语义事件 + 元数据 map | 面向 observability，非逐步迭代 |

### 2.2 设计决策表

| 决策 | GoForge | 对照框架 | 理由 |
|------|---------|---------|------|
| 事件载体 | 枚举 + tagged struct | Eino/ADK 也用 struct；tRPC 用阶段事件 | Go 惯用的 sum type，`switch ev.Type` 扁平好读，无接口层级 |
| 复用类型 | 复用 M1 的 `ToolCall/ToolResult/Usage` | Eino 自有 `schema.Message`；ADK 内嵌 SDK 类型 | 零新类型，Ring 1 → Ring 3 直接贯通 |
| 流式表达 | 逐事件 yield，不在 Event 里标流式 | Eino 用 `IsStreaming` + `MessageStream` | M3 范围只做非流式 `Chat`；流式留待需要时扩展 |
| 错误事件 | `EventError` + 迭代器第二返回值同时携带 err | Eino `Err` 字段；ADK `(Event, error)` | 双通道：只看事件的消费者也能从 `Content` 读到错误 |
| 步数字段 | `Step int` | 各框架有 `RunPath`/`InvocationID` 等更重的追踪 | 仅保留调试所需的最小信息，不做完整 trace（OTel 归 M7） |

### 2.3 为什么不用接口表达 sum type？

Eino 的 `TypedAgentEvent[M]` 是泛型 struct，用 `Output`/`Action`/`Err` 三个可空字段区分变体；ADK 用一个内嵌 `LLMResponse` 的大 struct。两者本质都是 tagged struct，而非接口多态。

GoForge 取同样路线但更轻：**一个 `EventType` 枚举 + 一组可空指针字段**。相比"每种事件一个实现 `Event` 接口的类型"，枚举式 sum type 的好处是：
- 消费者用一个 `switch` 就能穷举处理，编译器和 `default` 分支兜底
- 构造和零值都廉价，无需类型断言
- 与架构文档（`docs/ARCHITECTURE.md` Ring 3）规定的模型一致

代价是"非法状态可表达"（如 `EventThink` 却填了 `ToolCall`），靠构造器（`ThinkEvent`/`ToolCallEvent`/...）收敛，调用方不直接拼 struct。

---

## 3. Agent 接口：边界用接口，内部用具体类型

```go
type Agent interface {
    Run(ctx context.Context, task string) iter.Seq2[Event, error]
}

type SimpleAgent struct { /* llm, registry, system, model, maxSteps, toolTimeout */ }
func New(client llm.LLM, registry *tool.Registry, opts ...Option) *SimpleAgent
var _ Agent = (*SimpleAgent)(nil)
```

### 3.1 框架对照

| 框架 | 核心抽象 | Run 返回 | 流式载体 |
|------|---------|---------|---------|
| **GoForge** | `Agent`（1 方法） | `iter.Seq2[Event, error]` | Go 1.23 range-over-func 迭代器 |
| **ADK-Go** | `Agent`（含 SubAgents/FindAgent 等 6 方法） | `iter.Seq2[*session.Event, error]` | 同为 `iter.Seq2` |
| **Eino** | `TypedAgent[M]`（Name/Description/Run） | `*AsyncIterator[*TypedAgentEvent[M]]` | 无界 channel 包装的迭代器 |
| **tRPC-Agent-Go** | 无单一 Agent 接口，`StateGraph` 编译为 executor | 从有状态 context 拉取 | 图节点转移 |

### 3.2 设计决策表

| 决策 | GoForge | 对照 | 理由 |
|------|---------|------|------|
| 接口宽度 | 1 个方法 `Run` | ADK 6 个、Eino 3 个 | 窄接口原则；SubAgent/多智能体留给 M6，不提前进接口 |
| 返回类型 | `iter.Seq2[Event, error]` | 同 ADK；Eino 用自研 `AsyncIterator` | 标准库迭代器，`for ev, err := range` 即可消费，零自研并发原语 |
| 输入 | `task string` | Eino `TypedAgentInput[M]{Messages, EnableStreaming}` | 任务态 Agent；会话历史在 `Run` 内部构建，跨轮记忆留待会话层 |
| 具体实现 | `SimpleAgent` 具体类型 | Eino/ADK 也有具体实现 | 边界用接口、内部用具体类型；编排逻辑要可调试 |
| 配置 | 函数式 Option | 各框架 builder/option | 与 `pkg/llm/option.go` 风格一致 |

### 3.3 `iter.Seq2` vs Eino 的 `AsyncIterator`

Eino 自研了 `AsyncIterator[T]`——底层是无界 channel，`Next() (T, bool)` 拉取，需要配套生产者 goroutine 和关闭语义。

GoForge 直接用 Go 1.23 的 `iter.Seq2[Event, error]`（与 ADK-Go 相同选择）：

```go
for ev, err := range agent.Run(ctx, task) {
    if err != nil { /* 终止性错误 */ break }
    switch ev.Type { ... }
}
```

好处：**无需自研 channel 包装、无需手动管理生产者 goroutine 生命周期**。range-over-func 的"消费者提前 break"由语言机制传回生产者（yield 返回 false），我们在循环里逐处检查（见 §5.3）。这是把"框架自研的并发原语"换成"语言原生迭代器"的典型简化。

---

## 4. ReAct 循环：手写 ~85 行 vs 图编译

### 4.1 GoForge 实现（`SimpleAgent.Run` 内）

```
messages = [system?, user(task)]
for step in 0..maxSteps:
    resp, err = llm.Chat(ctx, messages, WithTools(registry.Schemas()...), WithModel?)
    if err: yield ErrorEvent(err); return
    messages = append(messages, resp.Message)
    if resp.Content != "":            yield ThinkEvent(...)        // 推理文本
    if resp.StopReason != ToolCall || no ToolCalls:
        yield ResponseEvent(...); return                          // 终止：最终答复
    for tc in resp.ToolCalls:         yield ToolCallEvent(tc)
    results = tool.ExecuteParallel(ctx, registry, resp.ToolCalls, toolTimeout)
    for r in results:                 yield ToolResultEvent(r); messages += ToolMessage(r)
yield ErrorEvent(ErrMaxStepsExceeded); return                     // 步数耗尽
```

### 4.2 框架对照

| 框架 | 循环形态 | 终止判定 | 迭代上限 | 规模 |
|------|---------|---------|---------|------|
| **GoForge** | 手写 `for` 循环 + `yield` | `StopReason != tool_call` 或无 tool_calls | `for step < maxSteps`，超出 → ErrorEvent | ~85 行 |
| **Eino** | 图编译（`compose.Graph` 节点 + 分支） | 流式分支消费消息看是否有 tool_calls | 模型节点前置 `getRemainingIterations()` 检查，默认 20 | `react.go` ~350 行 |
| **tRPC-Agent-Go** | Pregel/BSP 超步或 DAG | 节点返回路由命令 | 内含在图执行引擎 | 500+ 行（跨引擎） |
| **ADK-Go** | Workflow agent | Runner 驱动 | 框架管理 | 框架级 |

### 4.3 为什么坚持手写、不用图？

架构文档（`AGENTS.md` 核心设计原则 #4）明确：**"Hand-written ReAct Loop：~200 lines, no graph abstraction — simplest possible, best debuggability"**。

对照 Eino 的 `adk/react.go`：它把 init / chatModel / branch / cancelCheck / toolNode / afterToolCalls 等做成图节点，再编译执行。优点是可组合、可插拔回调；代价是要理解整个 `compose.Graph` 引擎（Eino 图引擎本身 2000+ 行）才能讲清"一次 ReAct 到底怎么走"。

GoForge 的取舍：**dev-workflow 不需要任意 DAG，ReAct 就是一个带条件退出的线性循环**。手写 `for` + `yield` 的可读性、可断点调试性远胜图编译，且总行数只有 Eino 的 1/4。迭代上限的处理思路则借鉴了 Eino——在每次模型调用的边界做守卫（Eino 在模型节点前置检查 `remainingIterations`，我们在 `for step < maxSteps` 上界 + 耗尽后发 `ErrMaxStepsExceeded`）。

### 4.4 终止与 think 事件的细节

- **终止判定**用 `StopReason != StopReasonToolCall || len(ToolCalls) == 0` 双重条件，对齐 Eino 流式分支"消费消息看有没有 tool_calls"的语义，但无需消费流。
- **ThinkEvent 仅在 `resp.Content != ""` 时发**：很多模型在请求工具时不带文本，此时不应发空 think 事件。
- **assistant 消息先入历史，再判终止**：保证下一轮 `Chat` 看到完整上下文（含本轮 tool_calls）。

---

## 5. 并行工具执行：`ExecuteParallel`

### 5.1 实现

```go
func ExecuteParallel(ctx context.Context, reg *Registry, calls []llm.ToolCall, timeout time.Duration) []llm.ToolResult {
    results := make([]llm.ToolResult, len(calls))           // 按输入顺序预分配
    g, ctx := errgroup.WithContext(ctx)
    for i, call := range calls {
        g.Go(func() error {
            callCtx := ctx
            if timeout > 0 { callCtx, cancel = context.WithTimeout(ctx, timeout); defer cancel() }
            results[i], _ = reg.Execute(callCtx, call)       // 失败已编码进 ToolResult
            return nil                                       // 不短路兄弟任务
        })
    }
    _ = g.Wait()
    return results
}
```

### 5.2 框架对照

| 框架 | 并发原语 | 结果顺序 | 单任务超时 | 失败策略 |
|------|---------|---------|-----------|---------|
| **GoForge** | `errgroup` | 预分配 `results[i]` 保序 | `context.WithTimeout` per tool | best-effort，错误进 `ToolResult.IsError` |
| **Eino** | `sync.WaitGroup` | 任务槽位 | 委托给 context deadline | panic 包装为 `PanicErr` |
| **tRPC-Agent-Go** | `sync.WaitGroup` | — | 模型执行层超时 | 可配 `enable_parallel_tools` |

### 5.3 设计要点

1. **保序无锁**：每个 goroutine 写自己的 `results[i]` 下标，互不重叠，无需锁——比"channel 收集再排序"更简单。
2. **回调恒返回 nil**：`errgroup` 的 `WithContext` 在任一返回非 nil 时会取消 ctx。我们**故意不让单个工具失败取消兄弟任务**（best-effort），所以错误不通过返回值传，而是 `reg.Execute` 已经把它编码进 `ToolResult{IsError:true, Content: err}`，回 LLM 观察后自行决定。
3. **per-tool 超时**：`timeout > 0` 时为每个工具派生独立 deadline，慢工具不拖垮整步；`<=0` 表示不限。这一点比 Eino/tRPC（多为整体或模型层超时）更细。
4. **保留 `ExecuteAll`**：M2 的单步 `-mode tools` 仍走顺序版，无破坏性改动。

### 5.4 errgroup vs WaitGroup

Eino/tRPC 都用裸 `sync.WaitGroup` + 手动 panic recover。GoForge 选 `errgroup`：虽然这里"恒返回 nil"用不到它的错误聚合，但它的 `WithContext` 语义清晰、代码更短，且 `golang.org/x/sync` 本就是间接依赖（openai-go 引入），提升为直接依赖零成本。`-race` 下测试验证了保序、失败隔离、超时三条路径并发安全。

---

## 6. M2→M3 衔接：类型与组件复用

```
M1/M2 资产                         M3 使用位置
──────────────────────────────────────────────────────────
llm.LLM.Chat()                     ReAct 循环每步调用
llm.Message / ToolMessage()        消息历史构建、observe 回填
llm.ToolCall / ToolResult / Usage  直接作为 Event 的载荷字段
llm.StopReasonToolCall             终止判定
llm.WithTools / WithModel          chatOpts() 组装
tool.Registry.Schemas()/Execute()  工具 schema 注入 + 执行
tool.ExecuteAll（保留）            单步 tools 模式继续使用
```

M3 没有改动 Ring 1/Ring 2 的任何公开类型——只在 `pkg/tool` **新增** `ExecuteParallel`。这再次验证内圈接口的前瞻性：Agent Runtime 完全站在既有抽象之上搭建。

---

## 7. CLI 集成：`-mode agent`

M3 的 Agent 通过 `internal/cli` 的 `-mode agent` 接入（CLI 本身在 M3 之后做了 Ring 5 结构化重构，详见 `progress.md`）。事件流渲染：

```
> 现在几点？把小时数乘以 3
[think] 我先查当前时间，再做乘法
  → current_time({})
  ✓ call_1: 2026-06-16T19:30:00Z
  → calculator({"a":19,"b":3,"op":"multiply"})
  ✓ call_2: 57
（最终答复）现在是 19 点，乘以 3 等于 57。
```

`handleAgentChat`/`agentTurn` 把 `EventThink/ToolCall/ToolResult/Response/Error` 分别渲染——这正是 sum type 在边界处的价值：一个 `switch` 覆盖所有变体。

---

## 8. 踩坑与设计要点

### 8.1 range-over-func 的"消费者提前 break"

`iter.Seq2` 的生产者必须检查每个 `yield(...)` 的返回值：消费者 `break` 时 `yield` 返回 `false`，生产者要立即 `return`，否则会继续调用已失效的 `yield`。循环里每处 yield 都写成 `if !yield(...) { return }`，并有专门的单测（`TestSimpleAgent_consumerBreaksEarly`）覆盖。

### 8.2 终止性事件的双通道

`EventError` 既作为事件（`Content` 含错误文本），又通过迭代器第二返回值传 `error`。这样"只 switch 事件类型"和"只看 err"两种消费风格都能正确终止。

### 8.3 maxSteps 守卫的位置

步数上限放在 `for` 上界，循环正常结束（未提前 return）即意味着耗尽，统一发 `ErrMaxStepsExceeded`。借鉴 Eino"模型调用边界检查迭代上限"的思路，但实现为最朴素的计数循环。

---

## 9. 当前未覆盖（留给后续 Milestone）

| 能力 | 当前状态 | 计划 |
|------|---------|------|
| file/shell 内置工具 + 沙箱 | 仅 calc + clock | M3-005（白名单目录 + 命令允许列表） |
| Token 计数 / 上下文压缩 | 仅 maxSteps 守卫 | M4（ContextPolicy 三级压缩） |
| 跨轮会话记忆 | 任务态 `Run(ctx, task)` | 会话层（M5+） |
| 流式 Agent 事件 | 非流式 `Chat` | 需要时扩展 |
| 多智能体 / SubAgent | 单 Agent | M6（Orchestrator-Worker） |
| OTel trace / span | 仅 `Step` 字段 | M7（Observability） |

---

## 10. 总结：GoForge M3 与框架的位置

```
                   Agent 运行时复杂度
                          ↑
tRPC-Agent-Go ───────────┤  StateGraph + Pregel/BSP 超步 + 路由命令
                          │  500+ 行执行引擎，面向任意工作流
                          │
Eino ────────────────────┤  compose.Graph 编译 + react.go ~350 行
                          │  AsyncIterator + IsStreaming 流式事件
                          │
ADK-Go ──────────────────┤  iter.Seq2 Agent + Workflow agents + SubAgents
                          │  富 session.Event 模型
                          │
GoForge M3 ──────────────┤  手写 ReAct for 循环 ~85 行，无图抽象
                          │  iter.Seq2[Event,error] + 枚举 sum type
                          │  errgroup 并行 + per-tool 超时
                          ↓
                    可调试 / 可读性
```

M3 的取舍与 M2 一脉相承：

- **手写 ~85 行 ReAct 循环**（vs Eino 图编译 ~350 行 + 2000+ 行图引擎）
- **`iter.Seq2` 标准库迭代器**（vs Eino 自研 `AsyncIterator`）
- **枚举式 Event sum type**（vs 接口多态或富元数据 struct）
- **0 个新 LLM 层类型**（完全复用 M1/M2）
- **errgroup + per-tool 超时** 的并行执行（比参考框架的整体超时更细）

这种"用语言原生能力替代框架自研抽象"的策略，把复杂度预算继续留给 GoForge 真正的创新战场——M4（Stage-Aware Context Loading）和 M5（Verification-Gated Pipeline）。ReAct 循环作为最内层的执行引擎，越简单、越可调试，越能支撑上层的编排创新。
