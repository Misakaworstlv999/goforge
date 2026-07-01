# M7: Steerable Run Control Plane — 设计笔记与框架对照

本文档解释 GoForge M7 的代码架构,并与 Temporal / LangGraph / Eino / tRPC-Agent-Go / ADK-Go 对照。
M7 的上游研究见 [`WORKFLOW_CONTROL_RESEARCH.md`](WORKFLOW_CONTROL_RESEARCH.md)(立项调研),本文记录**最终落地的实现与选型**。

M7 的核心命题:**一次 run 是可寻址、可观测、可操控、可回溯的资源,人(CLI/HTTP)与 agent(function call)用同一套语义驱动它。**

贯穿全程的铁律:**增量;零值=旧行为**(不接控制器 / 不配 telemetry / 不给子命令时,行为与 M6 逐字节一致);每步 `./init.sh` 全绿;脱敏(公共仓库,无内部标识,OTLP endpoint 不硬编码)。

---

## 1. 整体结构

```
pkg/pipeline/
├── control.go        # Control{Op,Stage,Note} + ControlOp + applyControl(stage 安全点)+ controlAbort + agentInterrupt(agent-step 安全点)
├── hub.go            # eventHub:事件扇出多订阅 + 历史回放(先 replay 后 live)
├── manager.go        # Manager:后台 run 注册表;Trigger/Subscribe/State/List/Pause/Resume/Steer/Redirect/Cancel/Rewind/Fork/Checkpoints
├── controltools.go   # ControlTools(m) → []tool.Tool:agent 即控制者(同语义)
├── checkpoint.go     # CheckpointStore:当前态投影(Save/Load/List)+ 血缘日志(SaveStep/LoadAt/ListCheckpoints)
├── sqlite.go         # checkpoints(最新指针)+ checkpoint_steps(血缘)两表
└── pipeline.go       # drive 循环:stage 安全点 + stage span;runControlled/runControlledFrom

pkg/server/http.go    # stdlib net/http + SSE,over Manager;WithLogger 注入
internal/cli/subcmd.go# serve/run/status/resume(stdlib flag + switch 分发)
internal/telemetry/   # OTel API+SDK+OTLP,no-op 默认;StartStage/StartLLM/StartTool/Init
internal/log/         # 窄 Logger 接口 + zap 默认 + Nop,构造器 DI
cmd/goforge/main.go   # 子命令分发;无子命令 → 不变的交互式 REPL
```

---

## 2. 控制核与安全点(P1)

一次 run 由后台 goroutine 驱动,`Manager` 持有 `runID → handle{control chan, eventHub, cancel, pipeline, done}`。控制是**协作式**的,在**安全点**消费单向 `control` 通道:

- **stage 安全点**:`drive` 循环顶部 `applyControl`。Pause→阻塞等 Resume/Cancel;Cancel→持久化+发 `EventCanceled` 终止;Redirect→改写下一 `CurrentStage`;Steer→写入黑板 `SteerKey`。
- **agent-step 安全点**(关键时效改进):`agentInterrupt(control)` 适配成 `agent.Interrupt`,在 ReAct 每步前被调用,使一次长 agent 运行**在推理步之间**就能响应 pause/steer/cancel/redirect,而非只在 stage 边界。Cancel/Redirect 经 `*controlAbort` 哨兵从 agent 透出,由 `drive` 翻译为终止 / 改道。

**零值不变**:`control == nil`(`Run`/`Resume` 传 nil)时所有安全点是 no-op,行为与 M5/M6 完全一致。`drive` 抽出 `runControlled`/`runControlledFrom` 两个入口,后者驱动一个已加载的 state(rewind/fork 的基础)。

`eventHub` 把事件**扇出多订阅**并保留历史,新订阅者先收到**回放快照**再转入 live —— HTTP SSE 与控制工具据此实现"迟到也能看全过程"。

---

## 3. Adapters:人与 agent 同语义(P2)

同一组 Manager 方法,两个 adapter:

- **HTTP**(`pkg/server`,stdlib net/http,Go 1.22 方法路由,SSE 用 `http.ResponseController.Flush`):`POST /v1/runs`、`GET /v1/runs[/{id}]`、`GET …/events`(回放+live)、`GET …/checkpoints`、`POST …/control`(pause/resume/steer/redirect/cancel/rewind/fork)、`GET /healthz`。
- **Agent-as-controller**(`controltools.go`):把控制面**内化为 builtin 工具集**(`trigger_run`/`list_runs`/`get_run_*`/`pause`/`resume`/`steer`/`redirect`/`cancel`/`list_checkpoints`/`rewind`/`fork`),进程内 supervisor agent 用 function call 操控 worker run。

**决策:不做 A2A、不做 MCP-server**。A2A 把 agent 当黑箱,承载不了 redirect/rewind/inspect;控制内化为工具即可让 agent 驱动 run,无需独立服务端(详见研究文档)。

---

## 4. 时间回溯:投影 + 血缘(P3)

唯一的 store schema 变更,刻意隔离在最后。`PipelineState` 增单调 `Seq`;`CheckpointStore` **新增**(保留旧方法):

- `Save`/`Load`/`List` —— **当前态投影**:每 run 一行可变指针,Resume/State/List 的 O(1) 热路径。
- `SaveStep`/`LoadAt`/`ListCheckpoints` —— **只增血缘日志**:每次转移一行(按 `Seq`),rewind/fork/time-travel 的底座。

这是有意的 **投影 + 日志**(event-sourcing 形状),不是冗余:投影可独立于血缘保留,日后血缘可裁剪/GC 而不丢当前态(`checkpoint.go` 接口注释明示)。`Manager.Rewind(id,seq,note)`(同 id 从某检查点重跑)、`Fork(src,new,seq)`(分叉独立 run),经 `replay` → `LoadAt` → 工厂建新 pipeline → `runControlledFrom`,note 注入黑板 `SteerKey`。

---

## 5. 生产化与选型(P4)

调研三个参考框架后逐项定型(出处见研究文档与各 commit):

| 关注点 | 选型 | 理由 / 对照 |
|---|---|---|
| CLI | **stdlib `flag` + `switch args[0]`**,非 cobra | tRPC-Agent-Go 真实二进制即此法;契合「零框架依赖」铁律。ADK 用 cobra(重)。无子命令 → 落到旧 REPL,保零值。 |
| 优雅退出 | `signal.NotifyContext` + `http.Server{ReadHeaderTimeout}` + `Shutdown(timeoutCtx)` | 与 tRPC openclaw 一致。 |
| 可观测 | **OTel API + SDK + OTLP exporter**(batteries-included),默认 no-op | 两家参考都默认 `noop` provider、显式 Init 才接 exporter。GoForge 选 batteries-included(开箱可采集),但 endpoint 空=纯 no-op、零开销。span 仅在 **Ring4 drive** 与 **Ring3 agent loop**(LLM+tool 调用点),**Ring1/2 不引 otel**,靠 context 嵌套 stage→llm/tool。 |
| 日志 | **zap**(遵 AGENTS.md)+ 窄 `Logger` 接口 + Nop | tRPC-Agent-Go 验证此型(接口+默认+noop+DI)。**只注入 Ring 5**(cli/server),不下沉内环,避免 pkg 内环耦合日志库。 |

**脱敏红线**:OTLP endpoint 仅来自 flag/env(默认空=不导出);README/文档无内部标识;`.mcp.json` 等永不入库;提交一律显式 `git add`。

---

## 6. 框架对照(控制面维度)

| 能力 | Temporal | LangGraph | Eino / tRPC / ADK | **GoForge M7** |
|---|---|---|---|---|
| 持久化 run | ✅ event-sourced | ✅ checkpointer | 部分(checkpoint) | ✅ 投影 + 血缘 |
| 寻址 / 列举 | ✅ | ✅ thread | 局部 | ✅ Manager + HTTP |
| 实时观测 | query | stream | callback / event | ✅ 事件 hub 扇出 + SSE 回放 |
| 暂停 / 注入 | signal | `Command{resume,update}` | 弱 | ✅ stage **及 agent-step** 安全点 steer |
| 改道 | — | `Command{goto}` | — | ✅ redirect(含 mid-agent) |
| 时间回溯 | reset | checkpoint lineage | — | ✅ rewind / fork |
| 人 = agent 同语义 | — | — | — | ✅ HTTP ↔ 控制工具同 Manager |

M7 把 Temporal 的「signal/query/durable run」与 LangGraph 的「checkpoint 谱系 / Command / time-travel」合成到 GoForge 的有环 FSM 上,并新增 **agent-step 粒度安全点** 与 **人/agent 同语义** 两点。

---

## 7. 控制面的语义边界(重要,使用方须知)

rewind/redirect/fork **恢复的是运行态,不是外部世界**。三者精确语义:

- **redirect**:同一 run 在途改道到某 stage,**不重载状态**(保留当前黑板);目标 stage 拿到 `StageOutput` 作为输入。
- **rewind**:同一 id,从某 checkpoint `seq` 重跑,**恢复该 seq 的黑板快照**(`runControlledFrom` → `State.Load(st.Blackboard)`),覆盖原前进态。
- **fork**:同 rewind,但**新 id**,原 run 不动。

上下文如何到达 LLM:每个 stage 的 agent 都是**现建**的(消息列表 = system + ContextPolicy.Sources + task),跨 stage 上下文**靠黑板 artifact,不靠对话历史**。因此 rewind/fork 恢复黑板 ⇒ 只要 stage 从黑板组装 prompt(M6 dev workflow / 通用 pipeline 皆如此),agent 就有连贯上下文。

三个已知问题(① 已修,②③ 留档):

- **① steer/rewind 指导必须被 stage 读入,否则对 LLM 无效 —— 已修。** OpSteer / rewind-note 写入黑板 `SteerKey`,但 stage 不读就等于没有。已提供 `SteerSource(state)`(control.go),通用 `agentPipeline` 已挂载;**自定义 RunAgent 类 stage 需在其 ContextPolicy 里 opt-in `SteerSource`**。测试 `TestSteerSource_reachesAgentPrompt` 证明指导进入 prompt。
- **② rewind(同 id)不截断 durable history(TODO)。** history 按 id append-only;若 stage 用了 `HistorySource`,rewind 重跑会读到「旧 transcript + 新 transcript」叠加,反而更乱。**当前建议:用了 HistorySource 的 pipeline 优先 `fork`(新 id、history 从空)而非 `rewind`。** 后续可给 rewind 加逻辑分界(记录 rewind 点,HistorySource 只读该点之后)。见 `Manager.Rewind` 处 TODO。
- **③ 不回滚文件系统等外部副作用(设计边界,非 bug)。** checkpoint 只快照 `PipelineState`(黑板 + FSM 位置),`write_file`/`exec_command` 改的是真实磁盘。rewind/fork 后**磁盘上已改的文件仍在**,重跑在"脏"工作区上继续;非幂等的文件操作可能二次叠加或基于错误状态规划。类似 Temporal:重放恢复工作流状态,不恢复它调用的外部世界。**干净重来需引擎外解决:fork 到独立 workdir / 幂等化 stage / 用 git 对工作区做外部快照。**

## 8. 不在范围 / TODO

- A2A、MCP-server(外部 agent 驱动我们的 run)—— 有具体外部集成需求再议。
- per-tool 细粒度 span(当前是 tool-batch span;细粒度需在 Ring2 加 hook,避免污染 Ring2)。
- 富并发冲突裁决、跨进程实时控制(单进程 Manager 起步;status/resume 经持久化 store 跨进程已够)。
- Webhook(M7-002):产品向,留待集成需求。
