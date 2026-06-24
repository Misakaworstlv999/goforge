# Workflow Control Plane — 研究与设计探索(M7 依据)

> 状态:**研究文档,非实现计划**。用于厘清"如何把一次工作流运行(run)对外暴露为可观测、可操控、可回溯的资源",
> 并据此确定 M7(Production Ready / 使用层)的范围。结论性范围决策见 §7。

## 1. 问题:run 对外是黑盒

M6 把"需求→设计→编码→评审→测试→验收"做成了有环图工作流,但**使用层**(怎么触发、怎么管)缺规划。
在 AI-native 理念下,工作流不应只由人(或迎合人类偏好的方式)触发,而应同时对 **agent 友好**地暴露。
"暴露成 API"看似够了,但暴露出真正的痛点——**一次 run 对外是黑盒**:

- 外部(人或 agent)触发后,**看不到中间过程与上下文**;
- **不能中途打断、纠偏**;
- 实现有问题需要返工时,往往**从头重来且上下文丢失**;
- 最终只拿到结果,不知道它是怎么来的、能不能改。

这是**控制面(control surface)**问题,不是传输协议问题。REST / A2A 只是传输。

### 1.1 关键澄清:最贵的零件 M5/M6 已经有了,只是没暴露、没泛化

| 痛点 | 引擎已具备(M5/M6) | 缺的是 |
|------|-------------------|--------|
| 看不到过程 | `Run/Resume → iter.Seq2[Event]`(stage 进入/产出、gate 结果、agent 思考/工具) | 没**流式对外 + 可重放** |
| 上下文在返工时丢失 | 有环 FSM 回边 + 共享黑板 + **无损 durable transcript** ⇒ 返工本就不重启、不丢上下文 | 对外**不可查询** |
| 不能打断 | HumanGate `interrupt → 持久化 → Resume(Decision)` | 只在**预声明的门**,不能"此刻暂停/改道" |
| 返工从头来 | SQLite checkpoint | `Load` **只取最新**,无 lineage ⇒ 不能 rewind / time-travel |

## 2. 现有技术对照

对"管理一个长时间运行的过程"这件事,横向比较 Go agent 生态与两个成熟范式。

| 能力 | Eino | tRPC-Agent-Go | ADK-Go | **Temporal** | **LangGraph** |
|------|------|------|-----|----------|-----------|
| run 可寻址 | 执行地址层级 | **requestID** | session id | **workflowID+runID** | **thread_id** |
| 观察 / 流式 | callbacks(单次,无多订阅) | iter + `RunStatus` 快照 | iter(单消费) | **完整事件历史 + 确定性重放** | **stream_mode: values/updates/messages/debug** |
| 多订阅 / 重放 | ✗ | ✗ | ✗ | ✓ | ✓ |
| 中断 / 恢复 | `StatefulInterrupt` + `ResumeWithData`(按 ID 定向) | 安全点 `EnqueueUserMessage` | 仅工具确认 | **Signal** | `interrupt()` + `Command(resume=)` |
| 改道(goto stage) | ✗(仅 `RerunNodes`) | ✗ | ✗ | (代码内) | **`Command(goto=)`** |
| 改状态(steer/update) | `StateModifier`(resume 时) | 仅排队用户消息 | ✗ | **Signal / Update** | **`Command(update=)` / `update_state()`** |
| 同步读状态 | ✗ | `RunStatus` | `session.Get` | **Query** | `get_state()` |
| checkpoint lineage / time-travel | ✗(单一最新,需手动版本化) | ✗ | ✗ | ✓ | **`get_state_history()` + 按 checkpoint_id 重入** |
| fork / branch | 手动多 ID | ✗ | ✗ | ContinueAsNew | ✓(从任一 checkpoint 分叉) |
| cancel | `interrupt()` + timeout | **`Cancel(requestID)`** | ctx cancel | Cancel / Terminate | ctx |

**结论**

- Go 三框架各有**碎片**:tRPC 的 `requestID + Cancel + EnqueueUserMessage + RunStatus` 最接近"管理在跑的 run";
  Eino 的 `ResumeWithData`(定向)+ `StateModifier` 最接近"带载荷的中断恢复"。
- 但**没有一个**同时具备:checkpoint lineage / time-travel、改道(goto stage)、多订阅 + 重放、任意点暂停。
- **Temporal**(可寻址 durable run + Signal/Query/Update + 历史重放)与 **LangGraph**(lineage + `Command{resume,goto,update}` + time-travel)
  合起来正是要的形状——但都不在 **Go agent 生态**,也都没把**人与 agent 视作对称的控制者**。

## 3. 创新论点(核心竞争力)

> **Steerable Run Control Plane**:把一次 run 变成**持久、可寻址、透明、可交互操控**的资源;
> **人与 agent 用同一套控制语义**;借 GoForge 的有环 FSM + checkpoint lineage 实现**精确返工与时间回溯**。

形式上:
**= Temporal**(可寻址 durable run + Signal/Query/Update + 历史重放)
**⊕ LangGraph**(checkpoint lineage + `Command{resume,goto,update}` + time-travel),
落在我们已有的 **cyclic-FSM / blackboard / durable-transcript** 之上,且**传输无关**(HTTP+SSE / A2A / CLI 都是它的 adapter)。

**差异化**:LangGraph(Python)已证明这类能力有价值,但 **Go agent 生态(Eino/tRPC/ADK)无人把它做成一等公民的可操控控制面**,
且它们经 A2A 把 agent 当**不透明黑盒**。GoForge 独到组合 = 回边(精确返工)+ 无损 transcript(不丢上下文)+ 分阶段 checkpoint lineage(回溯)。

## 4. 接口语义草图(传输无关的控制核)

一次 run = 资源 `runID`,两类操作:

**Observe(只读,≈ Query / stream)**
- `State(runID)`:当前 stage、status、blackboard 投影、acceptance 状态、retry 计数。
- `Transcript(runID)`:无损会话日志(durable log)。
- `Events(runID, fromSeq, follow)`:历史 + 实时事件;多订阅,**先回放后跟随**。

**Control(变更,≈ Signal / Update / Command)**
- `pause` / `resume`:泛化今天"仅在门处"的 resume;**协作式**,在 stage 安全点生效。
- `steer(note/constraints)`:注入黑板,下一/当前 stage 读取(≈ LangGraph `update` / tRPC enqueue)。
- `redirect(stage, note)`:改道到任意已注册 stage(直接用现成有环 FSM;≈ `Command(goto=)`)。
- `rewind(toCheckpoint, note)`:回到某分阶段 checkpoint 重跑(time-travel),**不丢上下文、不重启**。
- `fork(fromCheckpoint)`:从某检查点分叉出新 run(探索备选路径)。
- `cancel`。

**控制者对称**:人(UI / CLI)与 agent(supervisor agent / 经 A2A / 经一个 "control" MCP toolset)用**同一组操作**。
这是 AI-native 的关键:工作流不是 fire-and-forget,而是一个**可协作、可检视、可操控**的过程,人和 agent 平权。

## 5. 与 A2A / MCP 的关系(回答"A2A 能不能解决")

- **MCP** = agent ↔ 工具;**A2A** = agent ↔ agent,且**把对端 agent 当不透明黑盒**。
- A2A 给到:Task 生命周期(`working` / `input-required` / `completed`)、状态流、`tasks/cancel`、`input-required`(中途要输入)。
  → **A2A 能映射我们控制面的一个子集**:observe + resume-on-`input-required` + cancel。
- A2A **永远给不了**:redirect-to-stage、rewind、查看内部 stage —— 这正是用户要的"管理过程",而 A2A 设计上就不暴露内部。
- **定位**:控制面是我们自己的**协议无关核**;**A2A 是其中一个 adapter**(run → Task、HumanGate → `input-required`、events → status/artifact updates);
  **HTTP + SSE 是首个 adapter**,承载全部富操作(redirect / rewind / inspect)。A2A 客户端拿到能映射的子集即可。
- **反向对称**:一个 **A2A client**(把远程 A2A agent 包成本地 `agent.Agent`)是 MCP 工具客户端(M2-007)的 agent↔agent 孪生——
  让工作流某 stage 委派给远程 agent。可作独立小项。

## 6. 引擎缺口(都建在 M5 之上,均可增量加)

1. **协作式控制信号**:`pipeline.drive` 循环在 stage 安全点检查 per-run 控制通道(pause / cancel / redirect),保持"一次一 stage"的确定性。
2. **checkpoint lineage**:`Save` 改为按 step/stage **追加**快照(今为覆盖最新);新增 `Load(runID, atStep)` + `Fork` → 支撑 rewind / 分叉。
   ⚠️ 影响 M5 `CheckpointStore` 接口与 SQLite schema(需评估)。
3. **`Controller` API**(`pkg/pipeline`):`Pause/Resume/Steer/Redirect/Rewind/Fork/Cancel` + `State/Events` 订阅;均在安全点生效。
4. **Event hub**:把 `iter.Seq2` 扇出给 N 个订阅者 + 从 durable log 重放(迟到/外部订阅者先 history 后 live)。
5. **传输 adapter**:先 HTTP 控制 + SSE;再 A2A 映射子集。可观测性(OTel)顺带在 stage/agent/tool/LLM 安全点打 span。

## 7. 待定的范围决策(下一步要拍板)

1. **M7 定位**:以**控制面为中心**重构(HTTP/A2A 作 adapter),还是**先做 HTTP 透明层**(observe + resume 增量),
   把控制面(rewind / redirect / pause-anywhere)留作专门里程碑?
2. **A2A 实现**:**手写 stdlib 最小子集**(合"从零构建 / 零框架依赖"宪章)vs 官方 `a2aproject/a2a-go`(合 MCP-client 用官方 SDK 的先例)vs 暂不做。
3. **checkpoint lineage**:是否这轮就改 store schema?rewind 的语义与 UX(谁能 rewind、并发控制者冲突如何裁决)。
4. **控制面是否暴露为 MCP toolset**:让任意 MCP 客户端/agent 直接 trigger/observe/steer 我们的 run——
   这会把 M7-005("MCP server",原本可选)从"低价值"变成"有价值的统一入口"。

## 8. 参考

- Temporal:durable execution、Signal / Query / Update、Cancel/Terminate、event history & replay(workflow as addressable resource)。
- LangGraph:`checkpointer`(按 super-step 存档,thread 寻址)、`interrupt()` + `Command(resume/goto/update)`、`get_state_history()`(time-travel)、`update_state()`、`stream_mode`。
- 本仓库参考实现:`goforge-references/{eino,trpc-agent-go,adk-go}`(§2 对照的来源)。
- 现有引擎:`pkg/pipeline`(M5 cyclic FSM + checkpoint + interrupt/resume + event 流)、`pkg/agent`(durable transcript sink)。
