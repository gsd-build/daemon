---
name: gsd-browser
description: Use when a task needs browser automation, website interaction, rendered UI verification, navigation, form filling, screenshots, responsive testing, login flows, console/network inspection, visual diffs, prompt injection checks, or live browser observation.
---

# GSD Browser

Use `gsd_browser` when rendered browser behavior is evidence. Prefer normal code and terminal tools for source-code inspection.

## Workflow

1. Navigate with `method: "navigate"`.
2. Snapshot with `method: "snapshot"` before clicking or filling.
3. Use refs from snapshots for interactions.
4. Re-snapshot after navigation, submission, DOM changes, or modal changes.
5. Capture meaningful screenshots or snapshots when they help future review.
6. Check console and network when validating web app behavior.
7. State intent before multi-step browser work.

## Risk

Ask for approval before login submission, payment, checkout, destructive account changes, credential auth, external-effect actions, network mutation, or vault operations.

## Recovery

On stale refs, run a new snapshot. On navigation failure, inspect console/network and retry only when safe. On missing runtime, explain that the local machine needs `gsd-browser` installed or updated.
