# Managed Agent Input Authority

Automated text delivery is authoritative only when the daemon submits through
the exact managed agent thread while excluding human submission at the attached
terminal boundary. Terminal output, prompt glyphs, tmux capture, `send-keys`,
and paste buffers are not delivery acknowledgement.

## Runtime contract

For each leased intent, the daemon must:

1. Verify the durable and live pane, PID, and Codex thread incarnation.
2. Briefly freeze pane input while installing session-scoped tmux hooks.
3. Record every attached client's prior flags and make it read-only. New
   attaches and clients switched into the session are synchronously recorded
   and made read-only by the hook before tmux accepts their input.
4. Re-enable pane rendering/input processing only after every attached client
   is read-only, then revalidate identity and client flags at submission.
5. Submit the payload directly to the exact thread with Codex app-server
   `turn/start`; the stock TUI composer is never inspected or modified.
6. Treat the returned Codex turn ID as acknowledgement, remove the hooks, and
   restore only flags Azedarach changed while a pane-wide transition fence is
   active.

Only a matching non-empty turn ID advances the durable intent to delivered.
An authoritative active-turn rejection, stale identity, writable client, or
gate setup failure leaves the intent queued or stale. A disconnect, timeout,
malformed acknowledgement, or daemon crash after the durable `turn/start`
boundary leaves it ambiguous: it is never automatically submitted again, but
still expires at its original deadline. Gate state is written beneath the
daemon runtime directory so incomplete restoration remains diagnosable. Prompt
and composer content are never written to gate records or logs.

## Tool capability matrix

Codex is supported when `session.codexAppServer` is enabled. Azedarach launches
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
