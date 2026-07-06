# dec-2: Separate async mutation feedback from the footer

- Created: 2026-07-05
- Updated: 2026-07-05

## Rationale

Use four distinct surfaces because they serve different jobs: task cards show local truth and failure markers; task workspace/detail shows full explanation and recovery guidance; a floating notification stack shows transient cross-screen notices; notification history preserves overflow/auditability. The footer/status bar should only show compact attention counts or route to history, not full message bodies.

## Context

Issue csy exposed that optimistic async TUI mutations need clear feedback when daemon state lags or rejects a change. The existing footer/status-bar ticker behaves like a toast but is constrained to one line and one visible message, which truncates content and creates queueing/history problems.

## Consequences

Implementation must introduce task-local failure state separate from optimistic pending state, normalize daemon errors into user-facing messages, add responsive floating notification and history UI, and update status bar tests so full notification text is no longer expected in the footer.

## Links

- applies-to issue:csy
- applies-to issue:ctg
