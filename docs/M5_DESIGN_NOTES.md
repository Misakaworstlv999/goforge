# M5: Pipeline Engine — 设计笔记与框架对照

本文档解释 GoForge M5 阶段(Ring 4 流水线引擎)的代码架构,并与 Eino (ByteDance)、tRPC-Agent-Go (Tencent)、ADK-Go (Google) 逐层对照。

M5 的核心命题:**把多个 agent 阶段编排成可验证、可断点续跑、可人工介入的工作流——用"有环可路由的有限状态机(FSM)"而非图运行时,既支持条件分支/回边/环,又保住 `Stage[In,Out]` 的编译期类型安全。**

设计前先回答了两个关键问题(决定了 M5 形态),见 §2、§3。

---

## 1. 整体结构

M5 新增 `pkg/pipeline/`(核心 ~700 行 + 测试 ~700 行),并改了 `pkg/agent/` 两处(M5-003 的 `OnEvict` 接缝):

```
pkg/pipeline/
├── stage.go       # Stage[In,Out] + 类型擦除 node + Gate(Auto/LLMReview/Human) + RunAgent + 每阶段工具过滤
├── state.go       # 共享黑板 State + Reducer + StateSource(M4 接缝)+ PipelineState 快照
├── pipeline.go    # FSM 引擎:AddStage/Route/Run/Resume,顺序+条件+回边+重试+runaway 守卫
├── event.go       # pipeline.Event 标签联合(与 Ring 3 一致的流式模型)
├── checkpoint.go  # CheckpointStore 接口 + MemoryStore + gob 注册
├── sqlite.go      # SQLiteStore(modernc.org/sqlite,纯 Go,无 CGO):checkpoints/history/audit
└── *_test.go      # stage/state/pipeline/checkpoint/resume/audit/tools + 可运行 Example

pkg/agent/(改)
├── context.go     # ContextPolicy 增 OnEvict;compactMessages 丢弃前回调(durable-log 接缝)
└── agent.go       # 把 a.policy.OnEvict 接进压缩闭包
```

**默认零行为变更**:`agent.ContextPolicy{}` 仍是零值无操作;`OnEvict==nil` 时压缩行为与 M4 完全一致。

---

## 2. Q1:Pipeline 只能线性吗?——"FSM 本身就是有环有向图"

| 框架 | 编排模型 | 代价 |
|------|----------|------|
| **Eino** | 真图 `Graph[I,O]`:**Pregel**(允许环,`AnyPredecessor`)/ **DAG**(无环急切,`AllPredecessor`)双运行时;`Chain` 是 `Graph` 的语法糖;Branch/Parallel/fan-in 内建;`WithMaxRunSteps` 防失控 | 图引擎 ~2000 行;**边界用 reflection `checkAssignable` 校验类型,丢失泛型编译期安全** |
| **tRPC-Agent-Go** | `StateGraph` 全 DAG:`AddConditionalEdges`(按 state 路由)、`AddJoinEdge`(fan-in 栅栏)、BSP/DAG 双引擎;**图内无环**,环靠 `CycleAgent` 外包 | `State=map[string]any` + reflection,同样无静态类型 |
| **ADK-Go** | **完全没有图**:`SequentialAgent`/`ParallelAgent`(errgroup+分支隔离)/`LoopAgent`(maxIterations + `Escalate` 退出)组合树;分支靠 `transfer_to_agent` 工具 | 无泛型 |
| **GoForge M5** | **有环可路由 FSM**:命名 stage + 每 stage 一个 `RouteFunc`,可路由到任意已注册 stage(含更早的 = 回边);`WithMaxSteps` 防失控 | ~300 行引擎;**`Stage[In,Out]` 保持编译期类型安全** |

**关键洞察**:FSM 与"图"不是对立面——一个带条件转移的有限状态机**就是**一张有环有向图(重试=自环;评审打回编码=回边)。真正昂贵、且让 Eino/tRPC 丢掉静态类型的,是**并行 super-step 运行时**(Pregel/BSP)。M5 选择支持图拓扑(条件分支+回边+环),但**不建并行运行时**——这换来了三家参考框架都放弃了的差异点:类型安全的 `Stage[In,Out]`。

**并发边界(已确认)**:M5 纯顺序——任一时刻仅一个 stage 活跃,这让 checkpoint/resume/audit 的线性模型最简、最确定。已有的并发是:① stage 内工具并发(M3 `ExecuteParallel`);② 多 pipeline 实例各自 goroutine。**同一 pipeline 的阶段级并发(fan-out/fan-in)推迟到 M6**,届时用对 FSM 透明的 `ParallelStage` 组合件(ADK `ParallelAgent` 思路:errgroup + 分支隔离 + Reducer join),仍不建图引擎。

### 泛型 vs 动态路由的根本张力与解法

Go 泛型无法静态给"异构图"标类型(写不出 `[]Stage[?,?]`)。Eino/tRPC 都在图边界擦除成 `map[string]any` + reflection。**GoForge 的解法**:FSM 引擎对黑板/路由是动态、无类型的;**每个 Stage 通过 `compile()` 在自己的边界一次性 `type-assert In`、产出 `Out`**(类似 Eino 的 Pre/PostHandler、tRPC 的 input/output mapper)。类型擦除只发生在每个 stage 边界一次,不贯穿引擎——stage 业务逻辑全程强类型,引擎保持简单。

```go
type Stage[In, Out any] struct { Name string; Run func(ctx, In, StageDeps)(Out,error); ... }
func (s Stage[In, Out]) compile() (node, error) // node.run 签名是 func(ctx, any, StageDeps)(any,error)
func AddStage[In, Out any](p *Pipeline, s Stage[In, Out]) error // 自由函数:Go 方法不能带类型参数
```

---

## 3. Q2:agent 之间如何传递信息?——两条独立通道

三框架一致收敛到**两条通道**,M5 照此实现:

| 通道 | 含义 | Eino | tRPC | ADK | **GoForge M5** |
|------|------|------|------|-----|----------------|
| **横向类型化交接** | 上一阶段输出→下一阶段输入 | 边 + `FieldMapping` | `last_response` / I-O mapper | 子 agent 输出 | **`Stage[In,Out]`**:`advance` 把本阶段 `out` 作为下阶段 `StageInput` |
| **共享黑板** | 所有阶段读写的旁路 | `WithGenLocalState` + `ProcessState`(锁) | `State map` + 按键 `Reducer` | `session.State` + `StateDelta`(事件溯源,`app:`/`user:`/`temp:` 作用域) | **`State`**:并发安全键值区 + 按键 `Reducer` + `temp:` 不落盘 |

**读**走 M4 的 `ContextSource` 接缝(`StateSource(st, keys...)` 把黑板键渲染成注入消息),**写**走 stage 输出或 `deps.State.Set`——**Agent 接口一字未改**,共享是从 `ContextSource`(读)+ Stage 输出(写)组合出来的,不是新的 Agent 能力。借鉴点:tRPC 的**按键 Reducer**(`AppendReducer` 让重复写入累积而非覆盖,为 M6 fan-in 预留)+ ADK 的 **`temp:` 作用域**(`Snapshot()` 剔除,绝不进 checkpoint)。

### 每阶段工具子集(多 agent 分工)

共享一个 `Registry` 指针会让所有 `RunAgent` 阶段暴露**相同**工具集——多 agent 分工下不合理。M5 让每个 `Stage` 声明 `Tools []string` 白名单,引擎据此把共享 registry **过滤**成该阶段专属视图后注入 `StageDeps.Registry`:编码阶段拿 `read_file`/`write_file`,评审阶段只读 `read_file`。空 `Tools` = 全集(向后兼容);声明了不存在的工具 = 硬报错(阶段绝不静默少装备)。这与每阶段独立的 `ContextPolicy` 并列——都是"每阶段纵向独立"。

### 跨 stage 历史连续性:显式 opt-in(刻意区别于 ADK/tRPC)

**ADK/tRPC 默认自动共享整条 session**(下游 agent 自动看到上游事件,靠 branch/filterKey opt-out)。但本项目架构原则 #4 是 **"Stage-Aware Context Loading,而非被动累积"**——把 5 个阶段的 transcript 默认全量透传,正是它要反对的 lost-in-the-middle 累积。

GoForge 的取舍:**横向交接 `Stage[In,Out]` 只带类型结果,不带裸消息**;下游需要上游对话时,**显式**在自己的 `ContextPolicy.Sources` 里加 `HistorySource(store, pipelineID, limit)` 从持久化 transcript 拉取(默认不加 = 不带)。全史**可恢复、可共享**,但共享是每阶段主动选择。`HistorySource` 把 transcript **渲染成一条只读上下文消息**(而非注入裸 role 消息)——这样上一个 agent 的 `tool_call/result` 配对绝不会污染本 agent 的上下文(避免 OpenAI/Anthropic 400)。

---

## 4. 验证门 Gate(M5-001)

函数类型(轻量策略,延续架构文档),三种构造器:

```go
type GateStatus int // GatePass | GateFail | GateAwaitHuman
type Gate func(ctx, out any, deps StageDeps) (GateResult, error)
func AutoGate(check func(ctx, any) error) Gate          // 程序化:build/lint/test/Verify
func LLMReviewGate(client llm.LLM, criteria string) Gate // LLM 评判(首行 PASS/FAIL)
func HumanGate() Gate                                    // 返回 GateAwaitHuman → 引擎暂停
```

FSM 据 gate 结果决策:`GatePass`→路由;`GateFail`→同阶段重试至 `maxRetries`(超限 `ErrStageRetriesExceeded`);`GateAwaitHuman`→持久化 `StatusPaused`、终止本次 Run(发 `EventPaused`),等 `Resume`。

---

## 5. Checkpoint + Durable Log + OnEvict(M5-003)

| 决策 | 理由 |
|------|------|
| **`CheckpointStore` 接口 + 两实现** | `MemoryStore`(测试默认,gob 克隆存取,行为对齐落盘版)+ `SQLiteStore` |
| **`modernc.org/sqlite`(纯 Go)** | 满足 no-CGO/单二进制(`CGO_ENABLED=0 go build` 已验证);避开 `mattn/go-sqlite3` 的 CGO。AGENTS.md 明列 sqlite 为允许依赖 |
| **gob 序列化 + 公共类型注册** | `PipelineState` 经 gob 存 BLOB;`init()` 注册 string/int/float64/bool/[]any/map[string]any/[]llm.Message;自定义黑板类型由调用方 `gob.Register` |
| **`StageInput`/`StageOutput` 双存** | 暂停在 gate 后,既能"批准→用 `StageOutput` 向前路由",也能"拒绝→用 `StageInput` 重跑该阶段" |
| **durable log = 完整 transcript(append-on-produce)** | 三框架(ADK `session.Events`、tRPC `Session.Events`、Eino `state.Messages`)**一致**:完整对话是真相、全量持久化,压缩/摘要只是发给模型的**投影**。GoForge 据此:`agent.WithTranscriptSink` 在每条消息产生时(seed system/source/task、每轮 assistant、每个 tool result)即时回调,`RunAgent` 接到 `store.AppendHistory`——**存的是无损全量,发的是压缩投影**。压缩只重写发送切片,绝不流入 sink。`CheckpointStore.History(ctx,id)` 读回全量。`PipelineState` 快照不扛历史(只存 FSM 状态),transcript 独立增长在 `history` 表 |
| **(纠偏)放弃 `OnEvict` 作为日志写入口** | 早期实现用 `ContextPolicy.OnEvict` 只捕获"被淘汰的中段"——既不完整也不忠实(头部/最终尾部从不进日志,且 `RunAgent` 丢弃了 agent 消息切片)。与 M5-003"必须存完整历史"自相矛盾。已改为 append-on-produce,移除 `OnEvict` |

---

## 6. Interrupt/Resume(M5-004)与 Audit(M5-005)

- **Resume**:`store.Load` 还原 `PipelineState` + 把 `Blackboard` 灌回 `State`;`Decision{Approved}` 批准则 `advance` 向前路由,拒绝则 `bumpRetry` 重跑。在**新建的 pipeline 实例**上 Resume(模拟进程重启)是测试覆盖的核心场景。
- **Audit**:每次转移写 `AuditEntry{Timestamp,PipelineID,Stage,Action,Detail}`,Action ∈ `enter/gate_pass/gate_fail/retry/interrupt/resume/complete/fail`。SQLite `audit` 表按 `id` 保序。

---

## 7. 事件流(与 Ring 3 一致)

`Pipeline.Run/Resume` 返回 `iter.Seq2[Event, error]`,延续 M3 的拉取式流式模型,CLI/HTTP 可渲染进度。`Event` 是标签联合:`StageEnter/StageOutput/GatePass/GateFail/Retry/Paused/Done/Failed/Agent`。终止错误经迭代器第二返回值随 `Failed` 事件抛出。

---

## 8. 不在 M5 范围(明确推迟)

- **阶段级并发**(fan-out/fan-in)→ M6 `ParallelStage` 组合件,不建图引擎。
- **真实 workflow 阶段**(需求/设计/编码/评审/测试)→ M6。
- **HTTP `/resume`、cobra 子命令**(`goforge run/status/resume`)→ M7(Ring 5)。本里程碑用 `Example` + 测试演示,不加 CLI 模式。
- **跨阶段静态类型链接 combinator**:Go 泛型对异构链表达力有限;M5 用按名动态路由,stage 内强类型已足。

---

## 9. 与架构文档的偏差

- 架构文档把 `PipelineState.Data` 命名为 stage 输出表;M5 实现里**横向交接**走 `StageInput/StageOutput`(类型化),**共享黑板**走 `State`/`Blackboard`(命名键)——两条通道分开,语义更清晰。
- 架构文档 `Stage.Run(ctx, In, *Agent)`;M5 改为 `Run(ctx, In, StageDeps)`——`StageDeps` 携带 LLM/Registry/State/History,比裸 `*Agent` 更灵活(`RunAgent` 是便捷封装,stage 也可自建 agent)。
- 新增架构文档未列的:每阶段工具白名单、`OnEvict` durable-log 接缝、`WithMaxSteps` runaway 守卫、按键 `Reducer`——均为对照三框架后补强。
