# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Claude CLI's `-p --input-format stream-json` mode no longer exits immediately
  with `Error: Input must be provided either through stdin or as a prompt
  argument when using --print`. The v0.1.3/v0.1.4 executor attached a pty slave
  to BOTH stdin and stdout, which made `claude@2.1.96` detect an interactive
  session via `process.stdin.isTTY` and drop into a code path that ignores
  `--input-format` and insists on a positional prompt argument. The fix splits
  stdio asymmetrically: stdin is now a regular anonymous pipe
  (`cmd.StdinPipe`), so claude's `-p` mode accepts the stream-json payload
  normally, while stdout remains on the pty slave so Node still line-buffers
  output for real-time event delivery. `ptySysProcAttr` was updated to set
  `Ctty: 1` (previously 0) because the controlling tty fd index shifts when
  stdin moves off the pty. `Executor.Close` reverts to closing stdin for
  graceful shutdown now that stdin and stdout are distinct fds. A new
  regression test `TestExecutorStdinIsNotTTY` in `internal/claude/executor_test.go`,
  backed by a new `FAKE_CLAUDE_ASSERT_STDIN_PIPE=1` hook in `cmd/fake-claude`,
  pins the split-stdio invariant so any future change that re-attaches stdin
  to a pty trips at test time instead of silently shipping to production.
  Reproduced and verified against the production synthetic daemon on
  2026-04-08.
- Sessions no longer hang indefinitely when `Executor.Start` encounters an error
  during setup. Previously, `Start` could exit via three error paths
  (`openClaudePTY`, `cmd.StderrPipe`, `cmd.Start`) without closing the internal
  `ready` channel, causing any concurrent `Send` call to block forever. The
  symptom from the browser's perspective was: `taskStarted` frame received,
  followed by nothing — no stream events, no task completion, no error. The
  channel is now closed via a deferred guard on every exit path so `Send`
  always either succeeds or returns an error. The `session.Manager` goroutine
  that runs each actor now logs any non-nil `actor.Run` exit and synthesizes a
  `TaskError` frame to the relay for whatever task was in flight at the time,
  so failures are actionable from the browser instead of being invisible.
  `Executor.Start` now logs a single diagnostic line at entry, making it
  possible to distinguish "`Start` was never called" from "`Start` was called
  but failed deep inside" when triaging hung sessions from fly logs alone.
- Claude subprocess output is now flushed per event by allocating a
  pseudo-terminal for the CLI's stdout. Previously, Node.js's block-buffered
  pipe stdout meant that short responses (anything under ~8 KiB) never reached
  the daemon until claude exited, causing tasks to appear stuck indefinitely
  from the browser's perspective even though claude had completed the work.
  The daemon now attaches a pty to the child's stdin and stdout so Node detects
  a TTY and line-buffers output, flushing on every newline. A new regression
  test (`TestExecutorBlockBufferingFakeClaude`) using `cmd/fake-claude-blockbuf`
  exercises the exact mechanism and fails cleanly if the executor ever reverts
  to a pipe-based stdout. A standalone reproduction harness lives at
  `cmd/repro-stdout` for debugging similar stdio interactions with Node-based
  subprocesses.
- Claude subprocess failures now surface a descriptive error instead of a silent
  hang. Previously, any non-zero exit from the `claude` CLI (crashes, auth errors,
  rate-limit errors, config errors) was swallowed with no diagnostic output. The
  daemon now captures the last 50 lines (up to 16 KiB) of claude's stderr into a
  bounded ring buffer and includes the tail in the error returned from the session
  executor. Exits caused by normal shutdown (context cancellation or explicit
  `Close`) are still swallowed silently.
