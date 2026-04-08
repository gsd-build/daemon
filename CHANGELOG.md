# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Claude subprocess failures now surface a descriptive error instead of a silent
  hang. Previously, any non-zero exit from the `claude` CLI (crashes, auth errors,
  rate-limit errors, config errors) was swallowed with no diagnostic output. The
  daemon now captures the last 50 lines (up to 16 KiB) of claude's stderr into a
  bounded ring buffer and includes the tail in the error returned from the session
  executor. Exits caused by normal shutdown (context cancellation or explicit
  `Close`) are still swallowed silently.
