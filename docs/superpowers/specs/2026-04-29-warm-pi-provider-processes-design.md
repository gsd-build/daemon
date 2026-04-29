# Warm Pi And Provider Process Lifetimes

## Summary

GSD daemon task execution uses bounded warm process ownership. A session actor owns one Pi RPC worker for its active session/provider/config tuple. The Pi worker stays alive across sequential user turns, accepts one prompt frame at a time, and stops through deterministic lifecycle rules. Provider-local warm state lives inside the Pi extension:

- `claude-cli` owns a Claude Agent SDK worker backed by one long-lived async input stream.
- `codex-appserver` owns one `codex app-server --listen stdio://` child and one AppServer thread.

Warm workers are cache entries with explicit eviction, not permanent background daemons. Idle process trees stop after a 20-minute idle TTL, and the daemon keeps at most four idle warm workers by default. Active work remains governed by the task concurrency limit.

## Goals

- Remove routine per-message Pi process startup for active sessions.
- Keep Claude Agent SDK and Codex AppServer provider processes warm when their provider supports reusable turns.
- Preserve the relay-to-daemon task contract and Pi-owned tool/UI routing.
- Prevent unbounded local process residency on user machines.
- Keep cancellation, daemon shutdown, actor reaping, and memory-pressure cleanup deterministic.
- Provide testable worker boundaries for process reuse, restart, eviction, and crash recovery.

## Scope Boundaries

This spec covers daemon-side process ownership and provider lifetimes. Companion cloud-app work owns product UI for provider selection, session storage for provider/model pairs, and relay DB query updates that carry provider through dispatch and replay. The protocol contract includes `Task.Provider` with empty provider defaulting to `claude-cli`.

The daemon treats provider selection as a task execution input. When a task omits `Provider`, the daemon runs `claude-cli`.

## Research Basis

- Spike 016 validates one Pi RPC process accepting two prompt frames, emitting two `agent_end` events, and preserving Pi session context.
- Spike 017 validates one real Claude Code process handling two real Claude turns through one long-lived Claude Agent SDK `query()` stream.
- Spike 018 validates one warm Pi process keeping one Codex AppServer child and one AppServer thread alive across two prompt frames.
- Spike 014 validates Codex AppServer dynamic tools routing through Pi-owned tool execution and UI response flow.
- Spike 015 validates provider selection as `engine=pi` plus an additive `provider` field.

## Architecture

```text
session.Manager
  -> session.Actor(sessionID)
     -> pi.Worker(sessionID, provider, model, cwd, extension, skills, browser grant, custom instructions)
        -> pi -p --mode rpc -e <daemon extension> --provider <provider> --model <model>
           -> provider-local warm state
              -> claude-cli: ClaudeSdkWorker
              -> codex-appserver: Codex AppServer child + thread
```

The session actor remains the concurrency boundary. It accepts queued tasks through `SendTask`, marks a single in-flight task, and calls the Pi worker for execution. The worker rejects concurrent prompts. Each prompt call writes one Pi RPC `prompt` frame and streams Pi NDJSON until the matching `agent_end`.

The Pi worker owns:

- Pi RPC subprocess and process group.
- stdin/stdout/stderr pipes.
- event parser lifetime.
- task-bound prompt state.
- process PID callbacks and PID file integration.
- idle timestamp and last-used timestamp.
- deterministic stop and restart.

The session actor owns:

- task queueing and in-flight task state.
- relay forwarding and task completion handling.
- permission/question response routing.
- file activity capture.
- task cancellation signal.
- worker selection and eviction requests.

## Worker Identity

A worker is reusable only when the task execution key matches exactly:

- session id
- cwd
- Pi binary path
- Pi extension path
- provider
- model
- custom instructions
- selected skills
- browser grant id
- browser id
- browser session id
- plan capability presence and routing values

The browser grant tuple is part of the key because the extension reads browser access from process environment variables. A task with a different browser grant uses a restarted worker. A task with no browser grant uses a separate key from a task with an active browser grant.

The actor computes the desired key for each task. If the actor has a matching healthy worker, it reuses the worker. If the key differs, the actor stops the idle worker and starts a worker for the task key.

## Lifecycle Policy

Warm processes use two eviction layers.

### Actor Reaper

The session manager keeps its actor reaper. Actors idle for 30 minutes are stopped and removed. Actor removal stops any owned Pi worker.

### Worker Reaper

The daemon adds worker-level eviction inside the session manager:

- default worker idle TTL: 20 minutes
- default idle worker cap: 4 process trees
- minimum configurable idle TTL: 2 minutes
- maximum configurable idle TTL: 60 minutes
- minimum configurable idle cap: 0
- maximum configurable idle cap: 16

The idle cap counts only idle workers. Active workers are governed by `MaxConcurrentTasks`.

The worker reaper runs on a one-minute tick and also runs after task completion. Eviction order:

1. Stop idle workers older than the worker idle TTL.
2. If idle worker count exceeds the idle cap, stop least-recently-used idle workers.
3. If available memory is below the daemon safety threshold, stop idle workers before rejecting new work.

Idle cap `0` disables warm idle retention while preserving warm reuse within a single active task lifecycle. TTL `0` maps to the default 20-minute TTL.

## Process Ownership And Cleanup

Every Pi worker starts Pi in its own process group. Worker stop sends SIGTERM to the process group, closes stdin, waits for process exit, and sends SIGKILL after a short grace period. Provider children die with the Pi process group.

Worker stop runs on:

- idle TTL eviction
- idle cap eviction
- memory-pressure eviction
- actor removal
- daemon shutdown
- provider/model/config key mismatch
- Pi process exit
- parser failure
- unrecoverable provider error
- cancellation state uncertainty

PID tracking records the Pi leader PID for status and orphan detection. Cleanup treats the process group as authoritative rather than individual child PIDs.

## Prompt Execution Flow

```text
Task arrives from relay
  -> Actor computes worker key
  -> Actor starts or reuses Pi worker
  -> Actor sets task state and cancellation context
  -> Worker writes {"type":"prompt","id":"task-<taskID>","message":"..."}
  -> Worker parses Pi NDJSON
  -> Actor forwards translated stream events to relay
  -> Worker returns after agent_end
  -> Actor handles result and marks itself idle
  -> Worker remains idle until reused or evicted
```

The worker keeps stdin open between prompts. `extension_ui_request` handling remains synchronous during a prompt. The daemon writes `extension_ui_response` to the same worker stdin while the prompt is active.

The worker binds events to the active task by state machine, not by Pi event task id. It accepts one prompt at a time. If Pi emits `agent_end`, the active prompt completes. If stdout closes before `agent_end`, the active prompt fails and the worker stops.

## Cancellation

Task cancellation cancels the active prompt context. The worker first requests prompt interruption when Pi/provider exposes a prompt-level cancel mechanism. If the active prompt cannot be left in a known idle state, the daemon stops the worker process group and marks the task canceled.

Provider-specific cancellation:

- `codex-appserver` sends `turn/interrupt` when the provider has an active `threadId` and `turnId`.
- `claude-cli` aborts the active SDK turn. If SDK abort leaves stream ownership ambiguous, the Claude SDK worker stops and the next task starts a fresh provider worker.

Cancellation never keeps a worker whose transcript, tool bridge, or provider turn state is uncertain.

## Claude Provider Lifetime

The `claude-cli` provider owns a module-level `ClaudeSdkWorker` inside the Pi extension. The worker opens one Claude Agent SDK `query()` call with an async iterable prompt source and keeps that source open across provider turns.

Per Pi provider invocation:

1. The provider creates one Pi assistant stream for the current turn.
2. The provider sends only the new user turn into the SDK input stream.
3. The SDK output pump routes stream messages into the active Pi assistant stream.
4. The provider completes the Pi stream when the SDK emits the turn result.

Pi session context and the SDK process both maintain transcript state. The canonical runtime boundary for warm Claude is the provider process. The provider sends only the new turn to the long-lived SDK stream. The Pi session file remains the durable recovery source. Worker restart reconstructs state from the Pi session context through the normal provider prompt path.

Claude worker restart triggers:

- worker key mismatch
- model change
- allowed tool set change
- browser grant change
- custom instructions change
- SDK process exit
- SDK stream error
- cancellation uncertainty

The Claude SDK worker keeps the capabilities required by `claude-cli`: Pi MCP tools, permission handling, hooks, settings sources, `persistSession: false`, and configured Claude Code executable path.

## Codex AppServer Provider Lifetime

The `codex-appserver` provider owns one AppServer child and one AppServer thread inside the Pi extension. The provider starts AppServer with stdio transport, initializes once, calls `thread/start` once, and sends each Pi provider turn as `turn/start`.

Pi tools are mapped to AppServer `dynamicTools` during thread start. AppServer `item/tool/call` requests surface as Pi assistant tool calls. Pi executes tools and returns the tool result to the AppServer request so the same Codex turn can resume.

Codex worker restart triggers:

- worker key mismatch
- model change
- dynamic tool set change
- browser grant change
- AppServer process exit
- AppServer thread error
- cancellation uncertainty after `turn/interrupt`

The AppServer thread is ephemeral and local to the Pi worker. Pi session files remain the durable recovery source.

## Context And Recovery

Pi session persistence remains the durable transcript source for daemon recovery. Warm provider state is an optimization cache. If a worker stops, the next task starts a worker from the session file and provider context produced by Pi.

Compaction and context-stats requests use the same session file. If a warm worker is idle and healthy, daemon control operations may reuse it when Pi RPC supports safe control frames during idle. If safe reuse is unavailable for a command, the daemon uses the short-lived Pi control process shape.

## Capacity And Resource Safety

`MaxConcurrentTasks` controls active task concurrency. Warm idle workers do not increase active task capacity.

Worker eviction protects user machines:

- A user can run several active chats simultaneously up to the configured concurrency limit.
- Idle warm workers provide fast return to recent chats.
- Least-recently-used eviction prevents many recently touched chats from keeping many process trees alive.
- Memory-pressure eviction stops idle workers before the daemon rejects a task due to low memory.

The daemon status socket exposes worker counts:

- active workers
- idle workers
- configured idle TTL
- configured idle cap
- worker session id
- worker provider/model
- worker pid
- worker idle duration

## Configuration

Daemon config adds:

```json
{
  "warmWorkerIdleMinutes": 20,
  "warmWorkerIdleCap": 4
}
```

Effective defaults:

- `warmWorkerIdleMinutes`: 20
- `warmWorkerIdleCap`: 4

Validation clamps values to supported bounds. Logging records effective values at daemon startup.

## Error Handling

Worker errors map to task errors only when a task is active. Idle worker errors evict the worker and log a warning. A provider process crash during idle does not mark any task failed.

Active task failures include:

- Pi start failure
- provider start failure
- prompt write failure
- stdout parse failure
- provider stream error
- Pi exit before `agent_end`
- task timeout
- user cancellation

After an active task failure, the worker stops unless the worker proves it is in an idle and reusable state.

## Observability

Daemon logs include:

- `pi_worker_start`
- `pi_worker_prompt_start`
- `pi_worker_prompt_end`
- `pi_worker_stop`
- `pi_worker_evict_ttl`
- `pi_worker_evict_lru`
- `pi_worker_evict_memory`
- `provider_worker_start`
- `provider_worker_stop`
- `provider_worker_restart`

Log attributes include session id, task id when active, provider, model, pid, key hash, idle age, stop reason, and duration.

PID files continue tracking child process leaders. Worker start and stop callbacks update PID files for local status and orphan cleanup.

## Testing

Unit tests:

- worker key equality and mismatch cases
- idle TTL eviction
- idle cap LRU eviction
- memory-pressure eviction ordering
- worker stop kills process group
- actor removal stops owned worker
- cancellation stops uncertain worker state
- task failure stops worker
- idle provider crash removes worker without task failure

Integration tests:

- one Pi RPC process accepts two daemon tasks in the same session
- second task observes prior Pi session context
- actor reaper stops idle actor and worker
- worker reaper stops idle worker while actor remains available
- stop request cancels active task and leaves actor alive
- browser grant changes restart worker
- provider/model changes restart worker
- Codex AppServer provider uses one child/thread across two prompts
- Claude SDK worker uses one SDK subprocess across two prompts

Regression tests:

- `ask_human` and `ask_user_questions` still route through `extension_ui_request` and `extension_ui_response`
- file activity events still include touched file metadata
- context stats and compaction still read the correct session state
- daemon shutdown leaves no Pi, Claude, or Codex child processes

## Rollout

The implementation uses feature flags:

```text
GSD_WARM_PI_WORKERS=1
GSD_WARM_CLAUDE_SDK=1
GSD_CODEX_APPSERVER_PROVIDER=1
```

Rollout order:

1. Warm Pi worker with `claude-cli` provider stream behavior.
2. Worker TTL, idle cap, status reporting, and memory-pressure eviction.
3. Codex AppServer provider behind provider selection.
4. Warm Claude SDK worker inside `claude-cli`.
5. Default-on warm Pi workers after local verification and release soak.

Feature flags allow independent rollback of Pi worker reuse, Claude SDK reuse, and Codex provider exposure.

## Acceptance Criteria

- A session actor sends two sequential tasks through one Pi RPC process when the worker key matches.
- Idle Pi workers stop after the configured idle TTL.
- Idle worker count stays at or below the configured idle cap.
- Memory pressure stops idle workers before task rejection.
- Provider/model/browser grant changes start a worker with the matching key.
- Task cancellation leaves no uncertain reusable worker state.
- Daemon shutdown leaves no Pi, Claude, or Codex descendants.
- Codex AppServer provider reuses one AppServer child/thread across sequential turns in one warm Pi worker.
- Claude SDK provider reuses one Claude Code subprocess across sequential turns in one warm Pi worker.
- Task, question, file activity, compaction, and context stats flows keep their protocol shape.
