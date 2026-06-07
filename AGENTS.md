# AGENTS.md

GoForge — AI-powered development workflow engine built from scratch in Go.

## Startup Workflow

Before writing code:

1. **Confirm working directory** with `pwd` — must be `/Users/fengmoyuan/GolandProjects/goforge`
2. **Read this file** completely
3. **Read `docs/ARCHITECTURE.md`** for module design decisions and concentric-ring model
4. **Run `./init.sh`** to verify environment is healthy (includes desensitization scan)
5. **Read `feature_list.json`** to see current feature state
6. **Review recent commits** with `git log --oneline -5`

If baseline verification is failing, repair that first before adding new scope.

## Project Context

- **Language**: Go 1.24+ (leverages `iter.Seq2` for streaming; 1.24 required by anthropic-sdk-go)
- **Module path**: `github.com/Misakaworstlv999/goforge`
- **Local path**: `/Users/fengmoyuan/GolandProjects/goforge`
- **Reference code**: `/Users/fengmoyuan/GolandProjects/goforge-references/` (Eino, tRPC-Agent-Go, ADK-Go)
- **GitHub**: https://github.com/Misakaworstlv999/goforge

## Architecture: Concentric Ring Model

Five rings, inside-out. Inner rings have zero dependency on outer rings.

```
Ring 1 (core):  LLM Client       — Provider abstraction, streaming, retry
Ring 2:         Tool System       — Registry, JSON Schema gen, parallel executor
Ring 3:         Agent Runtime     — ReAct loop, ContextPolicy, compaction
Ring 4:         Pipeline Engine   — Stage FSM, verification gates, checkpoint, interrupt/resume
Ring 5 (edge):  Interfaces        — CLI, HTTP API, Webhook, MCP Server
```

Cross-cutting: `internal/config`, `internal/log` (zap), `internal/telemetry` (OTel)

See `docs/ARCHITECTURE.md` for full design rationale, interface definitions, and diagrams.

## Core Design Principles

1. **Stage-Aware Context Loading**: Each pipeline stage defines its own `ContextPolicy` (breadth, depth, sources) instead of passively accumulating and compacting
2. **Verification-Gated Pipeline**: Stages are separated by `Gate`s (auto/LLM/human) that must pass before progression
3. **Typed Pipeline via Generics**: `Stage[In, Out]` with compile-time type checking between connected stages
4. **Hand-written ReAct Loop**: ~200 lines, no graph abstraction — simplest possible, best debuggability
5. **Zero Framework Dependency**: No LangGraph, no Eino, no tRPC-Agent — only stdlib + minimal libs (zap, sqlite)

## Interface Design: Boundaries vs Internals

Derived from studying Eino (ByteDance) and tRPC-Agent-Go (Tencent):

> **Ring boundaries use interfaces; ring internals use concrete types.**

| Ring | Public Interfaces | Internal Concrete Types |
|------|-------------------|------------------------|
| Ring 1: LLM | `LLM` (Chat + ChatStream) | openai.Provider, anthropic.Provider |
| Ring 2: Tool | `Tool` (Name/Desc/Schema/Execute), `ToolSet` | Registry, schema reflector |
| Ring 3: Agent | `Agent` (Run -> iter.Seq2[Event, error]) | SimpleAgent (ReAct impl) |
| Ring 4: Pipeline | `Gate`, `CheckpointStore` | Pipeline FSM, SQLite store |
| Ring 5: Interface | — (entry layer) | HTTP handler, CLI |
| Cross-cutting | `EventHandler` (OnStart/OnEnd/OnError) | callback chain |

Principles:
- Interfaces are narrow (1-3 methods). If wider, split.
- Concrete types for orchestration logic — debuggability over abstraction.
- Function types for lightweight strategies (e.g., `CompactFunc`, `GateFunc`).
- Optional capabilities via type assertion, not forced interface embedding.

## Working Rules

- **One feature at a time**: Pick exactly one unfinished feature from `feature_list.json`
- **Verification required**: Don't claim done without running `./init.sh`
- **Update artifacts**: Before ending session, update `progress.md` and `feature_list.json`
- **Stay in scope**: Don't modify files unrelated to the current feature
- **Leave clean state**: Next session must be able to run `./init.sh` immediately

## Testing Rules

- **Every feature MUST include tests**: No feature is complete without corresponding `_test.go` files
- **Table-driven tests** for pure functions (type conversion, parsing, schema generation)
- **`httptest.NewServer`** for HTTP interaction tests — never depend on external APIs
- **Test naming**: `TestXxx_description`, subtests via `t.Run("scenario", ...)`
- **No test pollution**: Tests must not depend on ordering or shared mutable state

## Go Coding Standards

- Run `gofmt -s -w` on all modified files before committing
- Run `go vet ./...` to catch common issues
- Run `go build ./...` to verify compilation
- Use `zap` for structured logging — no `fmt.Println` in production code
- Error handling: always wrap errors with context (`fmt.Errorf("doing X: %w", err)`)
- Prefer table-driven tests
- Use Go generics where it improves type safety (Tool definitions, Stage pipeline)
- No CGO dependencies — single binary, cross-platform
- Keep packages small and focused; avoid god packages

## Desensitization Rules (MANDATORY)

This is a **public GitHub repository**. The following categories MUST NEVER appear in any committed file:

### Red Line (absolute ban)

1. **Employer / company names** and all known variants
2. **Specific product or game titles** (use "Game A", "Product B" etc. if needed)
3. **Internal system or platform names** (project management, code hosting, knowledge base, IM, etc.)
4. **Internal domains or IPs** (intranet URLs, internal API endpoints)
5. **Personnel info**: real names, employee IDs, corporate emails of colleagues
6. **Proprietary business logic**: specific implementations of account binding, virtual currency, proprietary API paths

### Green Line (allowed desensitized phrasing)

- "Large-scale backend system serving millions of concurrent users"
- "Multi-region deployment with strict zero-downtime requirements"
- "Microservice architecture with complex cross-service dependencies"
- "Safety-critical paths (account/payment) requiring strict verification"
- Generic terms: distributed locks, device fingerprinting, APM, canary release

### Enforcement

`./init.sh` reads `.sensitive-words` (gitignored, local-only) and scans all tracked files. Any match blocks the build. The `.sensitive-words` file contains the actual regex pattern and must be set up locally by each contributor.

## Required Artifacts

- `feature_list.json` — Feature state tracker (source of truth)
- `progress.md` — Session continuity log
- `init.sh` — Startup verification + desensitization scan
- `session-handoff.md` — Multi-session handoff context
- `docs/ARCHITECTURE.md` — Architecture design document

## Definition of Done

A feature is done only when ALL of the following are true:

- [ ] Target behavior is implemented
- [ ] Corresponding `_test.go` files exist with meaningful test coverage
- [ ] `go build ./...` passes
- [ ] `gofmt -l .` returns empty
- [ ] `go vet ./...` passes
- [ ] `go test ./... -count=1` passes
- [ ] Desensitization scan passes (no sensitive words)
- [ ] Evidence recorded in `feature_list.json` or `progress.md`
- [ ] Repository remains restartable from `./init.sh`

## End of Session

Before ending a session:

1. Update `progress.md` with current state
2. Update `feature_list.json` with new feature status
3. Record any unresolved risks or blockers
4. Commit with descriptive message once work is in safe state
5. Leave repo clean enough for next session to run `./init.sh` immediately

## Verification Commands

```bash
./init.sh
```

Required checks (run in order):
- Desensitization scan (sensitive word detection)
- `go build ./...`
- `gofmt -l .`
- `go vet ./...`
- `go test ./...`

## Escalation

If you encounter:
- **Architecture decisions**: Read `docs/ARCHITECTURE.md`, then check reference repos in `goforge-references/`
- **Unclear requirements**: Ask user
- **Repeated test failures**: Update progress, flag for human review
- **Scope ambiguity**: Re-read `feature_list.json` for definition of done
