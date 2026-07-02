# GoForge

An AI development-workflow engine built from scratch in Go — provider-agnostic
LLM client, a hand-written ReAct agent, a typed tool system, and a **steerable
pipeline control plane**, with zero heavyweight framework dependencies (stdlib +
`zap` + pure-Go SQLite + the OpenTelemetry API).

> Status: an active, milestone-driven learning/reference build. The control plane
> (M7) is the headline capability; see [`docs/WORKFLOW_CONTROL_RESEARCH.md`](docs/WORKFLOW_CONTROL_RESEARCH.md)
> for the design rationale.

## Why

Most agent frameworks treat a workflow run as a fire-and-forget call: once
triggered you cannot see inside it, pause it, nudge it, or rewind it — and a
mistake means restarting from scratch with the context lost. GoForge models a run
as a **durable, addressable, observable, steerable resource**, driven by the same
operations whether a human (CLI/HTTP) or an agent (function calls) is at the
wheel.

## Architecture: concentric rings

Inner rings never import outer rings.

```
┌───────────────────────────────────────────────────────────────┐
│ Ring 5  Interfaces      CLI · HTTP API · SSE                    │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ Ring 4  Pipeline      cyclic routed FSM · gates ·          │  │
│  │  ┌─────────────────── checkpoint lineage · control plane ┐ │  │
│  │  │ Ring 3  Agent        hand-written ReAct loop           │ │  │
│  │  │  ┌──────────────────────────────────────────────────┐ │ │  │
│  │  │  │ Ring 2  Tool      registry · JSON schema · exec   │ │ │  │
│  │  │  │  ┌────────────────────────────────────────────┐  │ │ │  │
│  │  │  │  │ Ring 1  LLM    provider abstraction · stream │  │ │ │  │
│  │  │  │  └────────────────────────────────────────────┘  │ │ │  │
│  │  │  └──────────────────────────────────────────────────┘ │ │  │
│  │  └───────────────────────────────────────────────────────┘ │  │
│  └──────────────────────────────────────────────────────────┘  │
│  Cross-cutting: internal/config · internal/log (zap) ·          │
│                 internal/telemetry (OpenTelemetry)              │
└───────────────────────────────────────────────────────────────┘
```

- **Ring 1 — LLM** (`pkg/llm`): provider-agnostic `Chat`/`ChatStream`, OpenAI &
  Anthropic backends.
- **Ring 2 — Tool** (`pkg/tool`): registry, reflection-based JSON-Schema
  generation, parallel executor, bulk scoping (`tool.Filter`), MCP client.
- **Ring 3 — Agent** (`pkg/agent`): a ~250-line hand-written ReAct loop (think →
  act → observe), with a step-granular cooperative control seam.
- **Ring 4 — Pipeline** (`pkg/pipeline`): a cyclic, conditionally-routed FSM of
  typed stages separated by verification gates, with a shared blackboard,
  checkpointing, interrupt/resume, **checkpoint lineage (time-travel)**, and the
  control plane (`Manager`, event hub, control signals).
- **Ring 5 — Interfaces** (`internal/cli`, `pkg/server`): the CLI and the HTTP +
  SSE control-plane API.

## Quickstart

```bash
# Build (pure Go, single binary, no CGO)
CGO_ENABLED=0 go build -o goforge ./cmd/goforge

# Configure a provider (flags or env; a .env file is also read)
export GOFORGE_PROVIDER=openai          # or: anthropic
export OPENAI_API_KEY=sk-...            # or: ANTHROPIC_API_KEY

# Interactive REPL (default — no subcommand)
./goforge -mode agent

# Trigger one run and stream its events
./goforge run "summarize the files in this directory"

# Start the HTTP control plane (persisted runs)
./goforge serve -http :8080 -store goforge.db

# Inspect / resume a persisted run
./goforge status -store goforge.db <run-id>
./goforge resume -store goforge.db <run-id>
```

Configuration resolves with precedence **defaults < `.env` < environment <
flags**. Run `./goforge -help` for the full flag list; key settings:
`-provider`, `-model`, `-mode {chat|tools|agent}`, `-http`, `-store`,
`-mcp-config`, `-otel-endpoint`, `-log-level`, `-log-format`.

## Control-plane API

A run is addressable by id and driven by the same verbs over three surfaces.

### HTTP (`goforge serve`)

| Method & path | Purpose |
|---|---|
| `GET /healthz` | Liveness check |
| `POST /v1/runs` | Trigger a run (`{"input": "..."}`) → `{"run_id": "..."}` |
| `GET /v1/runs` | List run ids |
| `GET /v1/runs/{id}` | Current state (status, stage, blackboard) |
| `GET /v1/runs/{id}/events` | Event stream (SSE): full replay, then live |
| `GET /v1/runs/{id}/checkpoints` | Checkpoint lineage (the seqs rewind/fork target) |
| `GET /v1/runs/{id}/transcript` | Reasoning transcript (LLM messages); `?format=text&level=final\|steps\|full` renders it |
| `POST /v1/runs/{id}/control` | Steer the run (see ops below) |

`POST /v1/runs/{id}/control` takes `{"op": "...", ...}`:

| op | extra fields | effect |
|---|---|---|
| `pause` / `resume` | — | hold at / release from the next safe point |
| `steer` | `note` | inject guidance into the run's shared context |
| `redirect` | `stage`, `note` | route to a specific stage (e.g. review → coding) |
| `cancel` | `reason` | stop the run |
| `rewind` | `seq`, `note` | re-run from an earlier checkpoint (same id, time-travel) |
| `fork` | `seq` | branch an independent new run from a checkpoint → `{"run_id"}` |

### Agent-as-controller (function calls)

`pipeline.ControlTools(manager)` returns a tool set — `trigger_run`,
`list_runs`, `get_run_state`, `get_run_events`, `get_run_transcript`,
`pause_run`, `resume_run`, `steer_run`, `redirect_run`, `cancel_run`,
`list_checkpoints`, `rewind_run`, `fork_run` — so an in-process supervisor agent
steers worker runs with the exact same semantics a human uses over HTTP. (No
separate MCP/A2A server needed.)

Crucially, `get_run_transcript` un-blackboxes a run: a controller can read the
worker's step-by-step reasoning, tool calls, and tool results (at `final`,
`steps`, or `full` verbosity) to diagnose *why* a run failed before deciding what
to steer, redirect, or inject.

### Safe points

Control is cooperative and applied at safe points: between pipeline stages **and**
between agent ReAct steps, so a long agent run reacts to pause/steer/cancel
promptly rather than only at stage boundaries. With no controller attached the
engine behaves exactly as an unmanaged run (the zero-value invariant).

## Observability

Telemetry is **off by default and zero-overhead** (the OpenTelemetry global no-op
provider). Point it at an OTLP collector to turn it on:

```bash
./goforge serve -otel-endpoint localhost:4318 -otel-insecure -service-name goforge
```

**Traces** — spans at pipeline-stage, LLM-call (with token usage), and tool-batch
boundaries, nested via `context`.

**Metrics** — `gen_ai.client.token.usage` (input/output), `gen_ai.client.operation.duration`,
`gen_ai.client.request.count`, and `goforge.pipeline.stage.duration`.

Both are exported over OTLP/HTTP to the same `-otel-endpoint`. Note the backend
split: **Jaeger ingests traces only** — to see the metrics you need a metrics
backend (e.g. Prometheus) behind an OpenTelemetry Collector that fans OTLP out to
both. A minimal setup:

```bash
# Traces only (quick): Jaeger all-in-one accepts OTLP on :4318
docker run -d -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one:latest

# Traces + metrics: run an OTel Collector on :4318 with an otlp receiver that
# exports traces → Jaeger and metrics → Prometheus (see the Collector docs).
```

Optionally attach LLM/tool payload previews to spans with
`-otel-body {off|preview|full}` (off by default). Structured logs use `zap`
(`-log-level`, `-log-format {console|json}`). The OTLP endpoint is read only from
flag/env — it is never hardcoded.

## Project layout

```
cmd/goforge        CLI entry point (subcommand dispatch)
internal/cli       composition + REPL + serve/run/status/resume
internal/config    layered configuration
internal/log       structured logging (zap) seam
internal/telemetry OpenTelemetry seam (no-op default, OTLP opt-in)
pkg/llm            Ring 1: LLM client + providers
pkg/tool           Ring 2: tool registry, schema, executor, MCP client
pkg/agent          Ring 3: ReAct agent
pkg/pipeline       Ring 4: pipeline FSM + control plane + checkpoint store
pkg/server         Ring 5: HTTP + SSE control-plane API
docs/              architecture & design research notes
```

## Development

```bash
./init.sh            # the canonical gate: scan + build + fmt + vet + test
```

`./init.sh` runs a desensitization scan, `go build ./...`, `gofmt -l .`,
`go vet ./...`, and `go test ./...`. This is a public repository; contributors
keep a local, gitignored `.sensitive-words` file that the scan enforces, and
secrets/endpoints live only in gitignored config (e.g. `.mcp.json`), never in
tracked files.

## License

See repository metadata.
