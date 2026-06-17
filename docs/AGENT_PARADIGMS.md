# Agent 范式:ReAct 之外 —— 设计取舍与框架对照

> 缘起:M3 实现了 ReAct 内循环后的一个问题——Plan-and-Execute、Reflection 这些范式考虑过吗?其他框架怎么做?本文把调研结论固化下来,作为 M5/M6 落地前的认知对齐。

---

## 1. 核心区分:范式分两层

最容易混淆的一点:这些"范式"不在同一抽象层级。

- **第 1 层 —— 单个 agent 的内循环**:think → act → observe。**ReAct 属于这层**,GoForge 已实现(`pkg/agent/agent.go` 的 `SimpleAgent`,~85 行)。
- **第 2 层 —— 多次 agent 调用的编排**:Plan-and-Execute、Reflection、Orchestrator-Worker 都属于这层。它们**不是"另一种内循环",而是对一个或多个 agent 的组合调用**。

**关键证据:连 Eino 都不是把 plan-execute 写成新循环,而是用工作流 agent 组合出来的。** `eino/adk/prebuilt/planexecute/plan_execute.go`(~880 行)的本质是:

```
Sequential(Planner) + Loop(Executor, Replanner)
```

即"规划 agent + 执行 agent + 重规划 agent"三个普通 `adk.Agent` 的编排。Reflection 在 Eino 里也是隐式的(藏在 Replanner 评估已执行步骤、决定 COMPLETE/CONTINUE 里),而非独立的 critic 循环。

**推论**:要支持这些范式,需要的不是"再写几种 agent 内循环",而是一个**组合层**——在 GoForge 里就是 Ring 4(Pipeline Engine)和 M6(Dev Workflow)。

---

## 2. 逐个范式

### 2.1 Plan-and-Execute

**是什么**:规划器 LLM 先产出多步计划,执行器逐步执行,必要时重规划(replan)。

| 框架 | 实现 | 形态 |
|------|------|------|
| **Eino** | ✅ 完整预置 `prebuilt/planexecute`(~880 行) | `Sequential(Planner)+Loop(Executor,Replanner)`,session key 传递 `PlanSessionKey`/`ExecutedStepSessionKey` |
| **ADK-Go** | ❌ 无预置 | 用户用 `SequentialAgent` + 自定义 agent 自行组合 |
| **tRPC-Agent-Go** | ⚠️ 仅 `planner/planner.go` 插件接口 | `Planner` 是图 agent 的 middleware(注入 instruction + 后处理响应),非独立 agent |

**GoForge 映射 → Ring 4 Pipeline(M5)**:**Pipeline 本身就是"计划"**。stages(requirement→design→code→review→test)是显式/人工编排的计划,FSM 执行,条件路由 + maxRetries = 重规划。这是有意的设计选择——dev-workflow 要的是**验证门把守的确定性阶段**,而非 LLM 临场自由发挥的步骤列表(后者可作为一个 requirement-analysis 阶段产出结构化 spec,供后续阶段消费)。

### 2.2 Reflection / Reflexion / Evaluator-Optimizer

**是什么**:agent 对自己的产出做批判,低于标准则重试/改进。

| 框架 | 实现 | 形态 |
|------|------|------|
| **Eino** | ⚠️ 隐式 | 藏在 Replanner 的进度评估里;无独立 critic 类型,reviewer 只是普通 agent 经 task tool 调用 |
| **ADK-Go** | ❌ 无 | —— |
| **tRPC-Agent-Go** | ❌ 无 agent 级反思 | `runner/ralph_loop.go`(~711 行)是 **runner 级**完成度校验(`CompletionPromise`/`VerifyCommand`/`Verifiers`),不是 agent 自评 |

**GoForge 映射 → Gate + FSM 回边(M6-004 Auto Review)**:review agent 给代码质量打分,低于阈值就 bounce 回 Coding 阶段。这**就是 reflexion**,但实现为 `Gate{Auto/LLMReview/HumanApproval}` + FSM 重试边,而非塞进 agent 内循环。Gate 抽象把"自动/LLM/人工"三种 review 统一起来。

### 2.3 Orchestrator-Worker(多智能体)

**是什么**:orchestrator 分解任务,spawn worker/sub-agent,agent 间 handoff/transfer,汇总结果。

| 框架 | 实现 | 形态 |
|------|------|------|
| **Eino** | ✅ 丰富 | `DeepAgent + AgentTool`(把 sub-agent 包成 tool,推荐)、`TransferToAgentAction`(不推荐)、Sequential/Parallel/Loop 工作流 agent(`adk/workflow.go` ~706 行) |
| **ADK-Go** | ✅ 轻量 | `Agent` 接口含 `SubAgents()`/`FindAgent()`;`SequentialAgent`(~90 行)、`agenttool` 包把 agent 当 tool |
| **tRPC-Agent-Go** | ⚠️ 图拓扑隐式 | `GraphAgent{graph, subAgents}`,sub-agent 是图节点,经边路由 transfer;无显式 orchestrator 类型 |

**GoForge 映射 → Go 并发(M6-006)**:orchestrator 分解任务,goroutine 并行 spawn worker,汇总。用 `errgroup`(我们 `pkg/tool/executor.go` 的 `ExecuteParallel` 已经在用这套)而非图引擎。

### 2.4 工作流 agent(Sequential / Parallel / Loop)

- **Eino**:✅ 显式三种(`adk/workflow.go`,单 `workflowAgent` + mode 开关,带 checkpoint/resume)。文档自己提示工作流 agent 非首选,优先 ChatModelAgent+AgentTool。
- **ADK-Go**:✅ 轻量 Sequential/Loop。
- **tRPC**:❌ 无显式类型,用图拓扑(DAG 节点/边、`ConditionalFunc` 路由)表达。

**GoForge 映射**:Sequential = Pipeline 的天然形态;Parallel = goroutine + errgroup;Loop = FSM 自环边。

---

## 3. 框架总览

| 范式 | Eino | ADK-Go | tRPC-Agent-Go | GoForge 计划 |
|------|------|--------|---------------|-------------|
| Plan-Execute-Replan | ✅ 完整(组合) | ❌ | ⚠️ 插件接口 | Pipeline = 计划(M5) |
| Reflection/Evaluator | ⚠️ 隐式 | ❌ | ❌ | Gate + FSM 回边(M6-004) |
| 多智能体编排 | ✅ DeepAgent+AgentTool | ✅ SubAgent 树 | ⚠️ 图拓扑 | Orchestrator-Worker goroutine(M6-006) |
| Seq/Parallel/Loop | ✅ 显式 | ✅ 轻量 | ❌(用图) | Pipeline / goroutine / FSM 自环 |

三种框架的路线:**Eino** 重抽象(预置一堆 agent + 组合原语);**ADK** 极简(agent 是迭代器、sub-agent 是树、agent 可当 tool);**tRPC** 图优先(多智能体从图拓扑涌现)。架构文档对照表已记录此立场:`Multi-agent | AgentTool/Flow | Team/Transfer | SubAgent tree | Orchestrator-Worker goroutine`。

> **术语澄清**:`feature_list.json` 中 M2 的 go_skill "Reflection" 指 Go 的 `reflect` 包(JSON Schema 生成),**不是** agent reflection,勿混淆。

---

## 4. 当前 `Agent` 接口需要返工吗?——不需要

```go
type Agent interface {
    Run(ctx context.Context, task string) iter.Seq2[Event, error]
}
```

这个窄接口**天然支持第 2 层组合**,因为第 2 层的每个参与者本身都是一个 `Agent`:

- **reviewer/critic 就是另一个 `Agent`**(换 system prompt + 工具)。Reflection = 在 Ring 4:跑 agent A → 跑 agent B(评审)→ 按分数决定是否重跑 A。编排在循环之外。
- **planner 也是一个 `Agent`**,输出结构化计划。
- **orchestrator-worker** = 多个 `Agent.Run` 跑在 goroutine 里。

所以待补的是 **Ring 4 Pipeline + Gate(M5)** 和 **M6 orchestrator**,让它们**组合** `Agent`。这与 Eino/ADK 同构(它们也是组合),区别只在:**GoForge 用显式 FSM + goroutine 组合,它们用图/工作流-agent 抽象**——延续"语言原生 > 框架抽象"的一贯取舍。

---

## 5. 诚实的缺口

1. **目前只有 `SimpleAgent` 一个 `Agent`,组合层(M5/M6)尚未建。** 准确状态是"接口已为之设计、范式已排进里程碑,但未落地",不是"已支持"。
2. **agent 内自反思 ≠ pipeline 级 review。** 当前 ReAct 循环不在 final response 前做显式自评。若需要,三个轻量入口:
   - (a) system prompt 约定(最轻,无代码);
   - (b) 包一层 `ReflectiveAgent`——在 `Run` 内先产草稿、自我 critique、再定稿,**纯组合不改接口**;
   - (c) 作为 Gate(归 M5)。
   倾向 (b)/(c),保持内核纯净。

---

## 6. 结论

ReAct 是**唯一的内循环范式**,刻意保持单一与精简。Plan-and-Execute、Reflection、Orchestrator-Worker 都是**外层编排**,GoForge 把它们映射到 Ring 4(Pipeline FSM / Gate,M5)与 M6(Dev Workflow),用显式 FSM + Go 并发组合既有的 `Agent`,而非引入通用图引擎或一堆预置 agent 类型。框架调研印证了这个分层:**这些范式本质是 agent 的组合,不是新的循环。** 当前 `Agent` 接口无需返工即可承载它们——这正是 M3 把接口设计窄的回报。
