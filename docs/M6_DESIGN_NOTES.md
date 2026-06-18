# M6: Dev Workflow — 设计笔记与框架对照

本文档解释 GoForge M6(Ring 4 应用层 `pkg/workflow/`)的代码架构,并与 Eino / tRPC-Agent-Go / ADK-Go 对照。

M6 的核心命题:**在 M5 流水线引擎上,把"需求→设计→编码→评审→测试→验收"建成一张有环图;验收点在需求阶段前置定义、作为契约贯穿全程;测试递进(unit→integration→e2e),验收由真实退出码背书。**

用户三条硬性要求贯穿设计,均已落地(见 §2/§3/§4)。

---

## 1. 整体结构

```
pkg/workflow/
├── types.go     # 验收契约:AcceptanceKind/Status/Point + Spec/Design/CodeChange/TestReport + acceptanceReducer + decodeJSON
├── stages.go    # 专职 agent 阶段(均 Stage[string,string])+ 黑板 artifact getters + 验收门/测试层
├── workflow.go  # BuildDevWorkflow 装配有环图 + RouteFunc 回边
└── *_test.go    # unit / integration / e2e 三层递进测试

pkg/pipeline/parallel.go  # ParallelStage 组合件(M6-006,对 FSM 透明的 fan-out/fan-in)
```

---

## 2. 图结构:有环可路由 FSM(要求 1)

```
requirement → techdesign → coding → review → test_unit → test_integration → test_e2e → acceptance → DONE
                              ▲         │          │              │              │            │
                              └─────────┴──────────┴──────────────┴──────────────┴────────────┘
                                          全部失败回边 → coding(rework hub);acceptance 未过也回 coding
```

- **coding 是 rework hub**:review、三个测试层、acceptance 失败都 `RouteFunc` 回边 coding。这是一张真正的有环图,
  M5 的 `RouteFunc`(可路由任意已注册 stage)直接表达,无需 LoopAgent/图运行时。`WithMaxSteps(60)` 兜底防失控。
- **两类决策的清晰区分**(关键设计):
  - **Verify / gate-fail-retry**:阶段校验**自己的产出**畸形 → 原地重试(requirement 无验收点 / techdesign 无文件)。
  - **RouteFunc 回边**:阶段判定**前序工作**不合格 → 去 coding 返工(review/测试层/acceptance)。
  M5 语义里 GateFail 是"重试本阶段",所以"评审失败去编码"必须走 RouteFunc 而非 GateFail——这是实现中纠正的一个要点。

| 框架 | 等价机制 |
|------|----------|
| Eino | LoopAgent(writer→reviewer)/ supervisor;图 + branch |
| tRPC | `AddConditionalEdges` 条件路由;环靠 CycleAgent 外包 |
| ADK | LoopAgent(maxIterations + Escalate 退出)|
| **GoForge** | **M5 有环 FSM 的 `RouteFunc` 回边**;每阶段独立 ContextPolicy + 工具子集 |

## 3. 类型化交接 vs 图:为何工作流用黑板做数据总线

M5 的 `Stage[In,Out]` 类型化交接对**线性链**完美(Out(前)==In(后)),但 M6 是**图**:`test_unit` 的 Out=`TestReport`
接不上 `test_integration` 的 In=`CodeChange`,回边到 coding 也类型不符。这正是 Eino/tRPC 用共享 State 做图数据总线的原因。

**GoForge 的做法(与 M5 叙事一致)**:工作流阶段统一为 `Stage[string,string]`(边只携带状态串,任意节点可互相路由);
**强类型 artifact 走黑板**(`spec/design/code/report/acceptance` 键 + 泛型 `getArtifact[T]` getters)。类型从"边"移到"黑板访问器",
和三家参考框架的"统一 State + 类型化访问"一致;M5 的类型化交接仍用于线性场景(如 M6-006 ParallelStage 内 In/Out 一致)。

## 4. 验收契约前置 + 真实退出码背书(要求 2)

- **需求阶段产出 `Spec.Acceptance []AcceptancePoint`**(`{ID,Description,Kind,Status,Evidence}`),`Kind` ∈ unit/integration/e2e/manual,
  自定义 JSON 编解码使其以可读字符串往返(LLM 友好)。需求门断言 ≥1 验收点,否则 gate-fail 重试。
- **契约存黑板**(key `acceptance`,`acceptanceReducer` 按 ID 合并):测试层后续 `Set` 部分更新(ID+Status+Evidence),
  保留 Kind/Description 与插入序。这是 M5 黑板 Reducer 的实战用例。
- **真实背书(防作弊)**:测试层**阶段本身**用 `exec_command` 跑真实 `go test`,据**真实退出码**(`exec_command` 在非零退出时
  追加 `[exit status N]`)判定 pass,再据此更新对应 Kind 验收点——**不信 LLM 自报**。终态 **acceptance 门要求每个点 Status==Pass** 才 DONE。
- 对照:tRPC 有最完整的 `Criterion`(ToolTrajectory/FinalResponse/LLMJudge)+ 分数阈值 LLM-judge;ADK 委托 runner;
  Eino 偏人工 interrupt。GoForge 取"**点粒度 + 自动化退出码背书**",比纯 LLM-judge 更硬。

## 5. 递进测试(要求 3)

**(a) 工作流内对生成代码**:三个测试节点 `test_unit → test_integration → test_e2e` 显式递进,每层一道(经 RouteFunc 的)门,
失败即回 coding;全过才进 acceptance。每层 `NewTestStage(kind, cmd)` 跑该层的真实命令。

**(b) 对 M6 自身**(`pkg/workflow/*_test.go` 三层):
- **unit**:`acceptanceReducer`、`decodeJSON`、各 stage 纯逻辑、reviewRoute(fake LLM,不落盘)。
- **integration**(`workflow_integration_test.go`):`BuildDevWorkflow` 全图 + 假 LLM(按 system prompt 路由)+ 假 exec,
  覆盖 happy path、**review 回边**、**测试失败回编码**——验证有环图闭环与验收门。
- **e2e**(`e2e_test.go`,真实、无外部依赖):coding agent **经 sandbox `write_file` 真写 .go 文件**,测试层**真跑 `go test`**,
  验收由真实退出码背书 → DONE;**失败回边变体**:首版 `a+b+1` → 真 `go test` 失败 → 回 coding → 改对 → 通过。
  (可选真 LLM 冒烟留 M7;`-short` 跳过 e2e。)

## 6. 多 Agent 编排(M6-006)

- 每阶段 = 专职 agent:独立 system prompt(`agent.BuildSystemPrompt` 角色)+ 独立 `ContextPolicy`(需求 wide/shallow、
  编码 narrow/deep)+ **工具子集**(M5 `Stage.Tools`:编码/测试拿 file+exec,**评审只读 read/list**——最小权限)。
- **`ParallelStage`**(`pkg/pipeline/parallel.go`):errgroup 并发子 stage + `join` 合并,**对 FSM 透明**(仍是单节点,
  checkpoint/resume/audit 保持线性)——兑现 M5→M6 延迟的并发项,**不建图运行时**。子 stage 经 reducer 守卫的黑板键做分支安全写入
  (深度 child-scope 隔离留后续)。orchestrator-worker 用例见 `parallel_test.go`(3 个 worker agent 并发实现、join 合成)。

## 7. 与架构文档的偏差

- 架构文档设想各阶段 `Stage[In,Out]` 异构类型链;实测图拓扑下类型接不上,改为统一 `Stage[string,string]` + 黑板 artifact 总线(§3)。
- "评审失败"用 RouteFunc 回边而非 GateFail(§2),因 M5 GateFail 语义是重试本阶段。
- 新增:验收点前置契约 + 退出码背书的验收门、三测试层节点、ParallelStage、orchestrator-worker——落实用户三要求。

## 8. 验证

`go test ./pkg/workflow/ -race` 覆盖 unit/integration/e2e;`-short` 跳过真跑 go test 的 e2e。`./init.sh` 全绿。
