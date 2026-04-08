# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

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
