# M4: Context Engineering — 设计笔记与框架对照

本文档解释 GoForge M4 阶段(Ring 3 上下文工程)的代码架构,并与 Eino (ByteDance)、tRPC-Agent-Go (Tencent)、ADK-Go (Google) 逐层对照。

M4 的核心命题:**给 ReAct 循环加上"短期记忆"管理——token 计数、按预算压缩、上下文注入接缝、system prompt 构建——默认零行为变更,可插拔启用。** 同时为长线产品的"长期记忆"留好接缝(M8)。

---

## 1. 整体结构

M4 在 `pkg/agent/` 新增/修改(核心 ~390 行 + 测试 ~310 行):

```
pkg/agent/
├── token.go         # TokenCounter 接口 + Estimator(RunesPerToken 可配) (79)
├── context.go       # ContextPolicy + ContextSource/CompactFunc 函数类型 + 三级压缩 (232)
├── prompt.go        # BuildSystemPrompt 纯函数 (76)
├── agent.go         # 改:WithContextPolicy + Run 注入 Sources + 预算压缩 (205)
└── *_test.go        # token/context/prompt + 集成(含配对校验) (~310)
```

**默认不改变行为**:`ContextPolicy{}` 零值 ⇒ 无 source、无压缩,现有 M3 测试全过。

---

## 2. 两条 token 路径(修正了一个想当然的错)

**关键澄清**:`llm.Message` **没有** Usage 字段,只有 `Response.Usage` 有。所以 token 信息分两路,不能混:

```go
// 路径 1:计数器(纯估算,作用于消息切片)
type TokenCounter interface { Count(messages []llm.Message) int }
type Estimator struct { RunesPerToken float64 } // 默认 4

// 路径 2:真实计数(预算触发用)—— ReAct 循环每次 Chat 返回的 resp.Usage.TotalTokens
```

### 2.1 框架对照

| 框架 | token 计数 | 可配 | 形态 |
|------|-----------|------|------|
| **GoForge** | **真实 `Usage` 优先**,`Estimator`(runes/4)兜底 | **`RunesPerToken` 可配**(中文调 ~2) | 窄接口,留待 tiktoken 替换 |
| **Eino** | API `Usage` 优先 + charLen/4 兜底 | 可插拔 `TokenCounterFunc` | 中间件注入 |
| **tRPC** | `SimpleTokenCounter{approxRunesPerToken:4}` | **可配**(注释明示中文 1–2) | 接口 + 默认实现 |
| **ADK** | 无(交给模型) | — | — |

### 2.2 设计决策

| 决策 | 理由 |
|------|------|
| **真实 `Usage` 作权威预算信号** | 循环每步 `Chat` 返回 prompt+completion = 已发送历史的真实 token(Eino "API 优先");`Usage` 由 agent 持有 `lastUsage`,**不进 `Message`、不进 `Counter` 接口** |
| **估算仅作兜底** | 首轮前 / Usage 为 0 时用 `Estimator` |
| **`RunesPerToken` 可配** | 英文 ~4、中文 ~2;**本项目中文内容,固定 /4 严重低估**(tRPC SimpleTokenCounter 同款) |
| **不用 tiktoken** | 其 BPE 文件/CGO 与 `AGENTS.md` 的 no-CGO+单二进制冲突;窄接口已留替换位 |

---

## 3. ContextPolicy:函数类型组合,不引入中间件

```go
type ContextSource func(ctx, task string) ([]llm.Message, error)  // 注入接缝(长期记忆入口)
type CompactFunc   func(ctx, client, messages, budget int) ([]llm.Message, error)

type ContextPolicy struct {
    MaxTokens, MaxMessages, RetainRecent int
    Sources  []ContextSource
    Breadth  Breadth   // Wide/Narrow/Medium
    Depth    Depth     // Shallow/Deep/Medium
    Compact  CompactFunc   // nil ⇒ DefaultCompact
    Counter  TokenCounter  // nil ⇒ Estimator
}
```

### 3.1 框架对照:压缩接到哪

| 框架 | 形态 | 触发 |
|------|------|------|
| **GoForge** | **内联 ReAct 循环 + 可插拔 `CompactFunc`** | 每步后 tokens(真实/估算)或消息数超限 |
| **Eino** | 中间件(`BeforeModelRewriteState` hook) | `TriggerCondition{ContextTokens, ContextMessages}` |
| **tRPC** | session 级摘要 + 独立 `TailoringStrategy` | 事件数/token/idle,或 auto 按窗口 50% |
| **ADK** | **无压缩**(`ContentsRequestProcessor` 只过滤组装) | 靠长上下文 |

**取舍**:GoForge 把压缩**内联**进手写循环,用函数类型(`CompactFunc`/`ContextSource`/`TokenCounter`)保留可插拔,**不引入 Eino 式中间件流水线**——延续 ReAct "无图、无中间件" 的极简立场(对照 `docs/M3_DESIGN_NOTES.md`)。

---

## 4. 三级压缩:middle-out + round-atomic(本milestone最关键的正确性)

`DefaultCompact` 三级,逐级直到预算内:
1. `dropOldToolResults` — 丢最旧**整轮**的 tool I/O(保 assistant 推理文本);
2. `truncateLongContent` — 截断超长 content + 省略提示(Eino reduction 的轻量版,不 offload);
3. `summarizeConversation` — 调 LLM 把**中段**压成一条摘要消息。

### 4.1 正确性约束:tool_call ↔ tool_result 配对不可破

OpenAI/Anthropic **强制**:带 `ToolCalls` 的 assistant 消息后必须紧跟每个 call 的 tool 结果。**朴素地"丢最旧的 tool 结果"会产生孤立 call/result → API 直接 400。** 因此:

- 压缩以**"轮"(round)为原子单位**:一轮 = 「assistant(可能含 ToolCalls)+ 其后所有 `RoleTool` 结果」。
- ① 丢整轮时,连同该 assistant 的 `ToolCalls` 一起清掉,**绝不留孤立项**。
- 区域切分 `splitRegions` 把 tail 的起点**从 tool 结果向前吸附到其所属 assistant**,保证 tail 自身配对完好。
- ③ 摘要把中段整体换成一条无 ToolCalls 的文本消息,天然无配对问题。

测试用一个 **`validatingMockLLM`** 模拟真 API:**任何破损配对的请求直接拒收**,在 agent 循环里实时校验——这是本阶段最值的一道防线。

### 4.2 其他约束

- **system 永不丢**、**首条 user(原始任务)保留**(head)、**最近 `RetainRecent` 条不动**(tail,默认 4,对齐 Eino `ClearRetentionSuffixLimit`)。
- **middle-out**:压中段、保 head+tail,对齐 tRPC `MiddleOut`(避免 "lost-in-the-middle")。
- **终止保证**:三级跑完仍超预算 ⇒ best-effort 返回不报错,绝不死循环。

---

## 5. ContextSource 接缝:长期记忆的入口(M8 预留)

`Run` 起始遍历 `policy.Sources`,在 system 之后、user task 之前注入。这是 M4 立下的**可插拔检索接缝**:

- **现在**:`StaticSource(msgs...)` 证明链路。
- **M8**:向量库/记忆检索就是又一个 `ContextSource`(ADK "前置检索" 形态);agent 主动检索则走 Ring 2 `Tool`(tRPC 形态)。

**source 失败 = 严格终止**(yield ErrorEvent):长期记忆是产品关键路径,静默丢检索结果会让 agent 在缺上下文下"自信地犯错"。

---

## 6. System Prompt Builder

`BuildSystemPrompt(role, tools, policy)`:角色 + 排序后的工具清单 + Breadth/Depth 自然语言指令。**纯函数、确定性**(同输入同输出,利于 prompt cache)。零 policy(wide+shallow)不加策略提示。

---

## 7. 有损压缩 与 持久化:当前缺口与归属

M4 的压缩是**纯有损**的——丢/截/摘后原始内容不留存。这是有意的 M4 简化。框架的做法(均"发送视图 reduced" ≠ "存储 lossless"):

| 框架 | 持久化 |
|------|--------|
| **Eino** `reduction` | **offload 到 Backend(文件/redis)** + 给 agent `ReadFileTool` 取回 |
| **tRPC** | session events 持久化(摘要是 overlay,原始不删) |
| **ADK** | session events 持久化,只过滤发送内容 |

**GoForge 的修法已排期(非现做)**:
- **M5-003 Checkpoint**:持久化**完整对话历史**(durable log)⇒ 压缩降级为"发送投影",原始可恢复;延后的 `ContextPolicy.OnEvict` 钩子在此落地。
- **M8-004**:被压内容 offload + embedding,给 agent 取回工具(Eino 模式)⇒ 有损压缩变"移至冷存储、可恢复"。

---

## 8. 纵向 vs 横向:agent 间记忆共享(归属 M5/M8)

M4 的 ContextPolicy 是**纵向独立配置**(单 agent 怎么用上下文)。**横向的 agent 间共享/传递不在 M4**,已显式注记落位:

| 子能力 | 归属 | 机制 |
|--------|------|------|
| 显式 handoff(A 输出→B 输入) | **M5-001** | `Stage[In, Out]` 类型化传递 |
| 共享黑板(多 agent 读写同一状态) | **M5-002** | `PipelineState` 命名键共享区(对标 ADK `session.State`、Eino `AddSessionValue`) |
| 语义记忆共享 | **M8** | 共享 `MemoryStore` |

**关键**:`Agent` 接口不变——共享由 **ContextSource(读)+ Stage 输出/memory 工具(写)** 组合而成,不是新增 agent 能力。

---

## 9. 当前未覆盖(留给后续 Milestone)

| 能力 | 当前 | 计划 |
|------|------|------|
| 压缩内容持久化 / offload | 纯有损 | M5-003(durable log + OnEvict)、M8-004(取回工具) |
| agent 间共享/传递 | 仅 per-agent 纵向 | M5-001/002(handoff + 黑板)、M8(语义) |
| per-stage ContextPolicy 消费 | 接缝已立 | M5 Pipeline |
| 精确 token 计数 | 估算器 | tiktoken(接口已留,受 no-CGO 制约) |
| auto 上下文窗口检测 | 固定 MaxTokens | 可选后续(需 model→窗口 注册表) |

---

## 10. 总结:GoForge M4 与框架的位置

```
                   上下文工程复杂度
                          ↑
Eino ────────────────────┤  summarization + reduction 双中间件 + offload 到存储
                          │  BeforeModelRewriteState hook,可配 TokenCounterFunc
                          │
tRPC ────────────────────┤  session 级摘要 + MiddleOut/Head/Tail Tailoring
                          │  SimpleTokenCounter(runes 可配)+ auto 窗口检测
                          │
GoForge M4 ──────────────┤  内联 ReAct 循环 + 可插拔 CompactFunc/ContextSource/TokenCounter
                          │  三级 middle-out round-atomic + Usage 优先估算兜底
                          │  ContextSource 接缝(长期记忆入口)
                          │
ADK ─────────────────────┤  无压缩(ContentsRequestProcessor 只过滤组装)
                          ↓
                    可调试 / 可读性
```

M4 延续 M1–M3 的取舍:

- **内联 + 函数类型**(vs Eino 中间件流水线)——压缩/计数/注入皆可插拔,但无框架机器。
- **真实 `Usage` 优先 + 可配估算兜底**(中文 runes 比例必需)——汲取 Eino+tRPC,绕开 tiktoken 的 CGO 代价。
- **round-atomic 压缩**——一个被三家(隐式)依赖、却最容易写漏的正确性约束,我们显式守住并用模拟 API 测试。
- **接缝先行**:`ContextSource`(长期记忆)、持久化归属(M5/M8)、agent 间共享(M5/M8)全部**点名预留**,而非现做——把复杂度预算继续留给 Pipeline(M5)与记忆(M8)。
