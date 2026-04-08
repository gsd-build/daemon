# repro-stdout

Standalone reproduction harness for the "claude subprocess stdout silent" bug
that motivated the pty path in `internal/claude/executor.go`.

## What it proves

The daemon used to wire the claude CLI's stdin/stdout through anonymous pipes.
Claude is a Node.js program, and Node's libuv stream layer block-buffers
`process.stdout` when it is not a TTY: short writes (anything under ~4–8 KiB)
sit in a userspace buffer and never reach the kernel pipe until the buffer
fills or the process exits. For a minimal assistant response (~200 bytes of
stream-json), the buffer never fills, the daemon's parser reads zero bytes,
and the task appears stuck indefinitely from the browser's perspective.

The fix is to give claude a pseudo-terminal for stdout. Node detects a TTY
and switches to line buffering, flushing on every `\n`, and the daemon sees
events in real time.

## Usage

Build first, then run either against the real claude binary or against one
of the test fakes:

```
$ go build -o /tmp/fake-claude-blockbuf ./cmd/fake-claude-blockbuf
$ go run ./cmd/repro-stdout --mode pipe --bin /tmp/fake-claude-blockbuf
mode=pipe bin=/tmp/fake-claude-blockbuf events=0 firstThree=[]
exit status 1

$ go run ./cmd/repro-stdout --mode pty --bin /tmp/fake-claude-blockbuf
mode=pty bin=/tmp/fake-claude-blockbuf events=3 firstThree=[stream_event assistant result]
```

The same demonstration works against a real claude binary on any
Linux or macOS host with `@anthropic-ai/claude-code` installed and
`ANTHROPIC_API_KEY` set.

## Exit codes

- `0` — at least one stream-json event was read within the timeout
- `1` — the timeout elapsed with zero events (the bug)
- `2` — the harness itself failed to spawn the subprocess or configure stdio

## Why it is committed

This binary is permanent. It serves two purposes:

1. **Living documentation.** Future maintainers investigating a similar
   stdio interaction with a Node-based subprocess can read this code and
   see exactly how the pipe-vs-pty difference manifests, without having
   to re-derive the diagnosis from scratch.

2. **Regression reference.** The `internal/claude` test suite replicates
   the mechanism via `cmd/fake-claude-blockbuf`, but this binary is the
   simplest possible end-to-end reproduction and can be pointed at a
   real claude binary in a Dockerfile debugging session, on a Fly VM,
   or anywhere else the unit tests cannot reach.

Do not delete it.
