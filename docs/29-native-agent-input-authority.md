# Managed Agent Input Authority

Automated text delivery is authoritative only when the daemon submits through
the exact managed agent thread while excluding human submission at the attached
terminal boundary. Terminal output, prompt glyphs, tmux capture, `send-keys`,
and paste buffers are not delivery acknowledgement.

## Runtime contract

For each leased intent, the daemon must:

1. Acquire and renew one durable session-scoped lease so separate daemon
   processes, intent keys, and old/new incarnations cannot overlap one
   managed-input gate. Incarnation and fence token are exact lease values.
2. Verify the durable and live pane, PID, and Codex thread incarnation.
3. Briefly freeze pane input while installing session-scoped tmux hooks.
4. Record every attached client's prior flags and make it read-only. New
   attaches and clients switched into the session are synchronously recorded
   and made read-only by the hook before tmux accepts their input.
5. Atomically renew the exact session fence while crossing the durable
   ambiguous-submission boundary, then revalidate live pane/client state,
   hook-bound thread incarnation, and the exact durable fence again.
6. Submit the payload directly to the exact thread with Codex app-server
   `turn/start`, bounded to finish before the renewed lease deadline; the stock
   TUI composer is never inspected or modified.
7. Treat the returned Codex turn ID as acknowledgement, remove the hooks, and
   restore only flags Azedarach changed while a pane-wide transition fence is
   active.

Only a matching non-empty turn ID advances the durable intent to delivered.
Stale identity, writable clients, or gate setup failure proven before RPC may
leave the intent queued or stale. Every `turn/start` JSON-RPC method error,
disconnect, fence-renewal loss/timeout, acceptance timeout, malformed
acknowledgement, or daemon crash after the durable submission boundary leaves
it ambiguous because dispatch may already have had an effect: it is never
automatically submitted again, but still expires at its original deadline.
Gate state is written beneath the daemon runtime directory with durable
project/session/incarnation/owner/fence identity. Normal restoration requires
the exact live fence; startup recovery atomically claims only an expired or
unowned matching fence, and a takeover restores its predecessor before
installing a new gate. Restoration deletes the event ledger first and the
durable state file last; that state file is the authoritative completion
marker, so any cleanup failure retains the exact session fence. Prompt and
composer content are never written to gate records or logs.

## Tool capability matrix

Codex is supported only when `cliTool: codex` and `session.codexAppServer: true`
are both configured. Azedarach launches
the stock remote TUI (`codex --remote unix://`) under a supervised local
app-server daemon. Hooks bind the managed pane identity to the exact Codex
thread ID. Automated turns use a separate app-server proxy only while tmux has
made every attached managed client read-only. A pre-existing draft remains
local to the stock TUI and is byte-for-byte untouched.

Standalone Codex, Claude, OpenCode, and interactive shell clients are not
supported for authoritative automated input. They do not expose a shared turn
API paired with the managed tmux input gate, so they fail closed.

This unsupported state is intentional product behavior. Adding a tool adapter
requires production evidence that its attached human client and API satisfy
every step above; a terminal injection adapter cannot qualify. Raw same-user
tmux access that bypasses Azedarach is outside the managed guarantee.
