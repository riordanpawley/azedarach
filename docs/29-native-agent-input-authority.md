# Native Agent Input Authority

Automated text delivery is authoritative only when a native agent client shares
its turn/composer arbiter with Azedarach. Terminal output, prompt glyphs, tmux
capture, `send-keys`, paste buffers, and a second resume process are not delivery
acknowledgement.

## Runtime contract

The daemon listens on the permission-restricted Unix socket exported to managed
sessions as `AZEDARACH_AGENT_INPUT_SOCKET`. A conforming native client writes one
newline-delimited JSON registration before receiving deliveries:

```json
{"protocol_version":1,"project_id":"...","session_id":"...","logical_pane_id":"agent","tmux_pane_id":"%12","pane_pid":123,"agent_incarnation":"...","tool":"codex"}
```

Registration must identify the exact durable managed-agent incarnation. The
daemon rejects a missing or stale registration before disclosing a payload.
For each leased intent it sends a newline-delimited envelope containing the
exact project, session, intent, incarnation, lease, message kind, and payload.

While holding the same lock used by its attached human composer, the native
client must atomically:

1. Verify the registered incarnation is still current.
2. Prove its tool-owned composer is empty.
3. Exclude attached human input through native turn submission.
4. Submit the payload at most once for the intent/incarnation pair.
5. Return the same non-empty acknowledgement token on every retry.

The response echoes `project_id`, `intent_key`, `agent_incarnation`, and
`lease_token`, and reports one outcome: `accepted`, `composer_nonempty`,
`human_attached`, `stale_incarnation`, or `not_ready`. Only an exact `accepted`
response with a non-empty acknowledgement advances the durable inbox intent to
delivered. Durable inbox acceptance is exactly-once, but Codex submission is
at-most-once: after a crash where `turn/start` may have been accepted, the
pending intent remains queued and is never automatically resubmitted. The
client surfaces an explicit recovery action to inspect the thread and recover
or discard the intent. Disconnects, timeouts, malformed responses, and refusal
outcomes leave it queued (or mark an explicitly stale incarnation).

Recovery is restricted to the same managed worktree/session and exact persisted
thread/incarnation. After inspecting the thread, run `az ai native-codex-client
--recover-intent <intent> --recover-thread <thread> --recover-action
delivered|discard`. Both actions are audited and never submit to Codex.

Frames are newline-delimited JSON, protocol version 1, with a 4 MiB maximum.
The socket is mode `0600`; protocol peers are trusted components running as the
same OS user, not a cross-user security boundary.

## Tool capability matrix

Codex is supported when `session.codexAppServer` is enabled. Azedarach launches
`az ai native-codex-client` instead of the stock remote TUI. That client owns
the visible human composer, submits both human and automated turns through the
app-server protocol, registers the exact tmux/PID/incarnation identity, and
persists intent acknowledgements before replying so supervised reconnects do
not duplicate accepted turns. A non-empty human draft is preserved and causes
automated delivery to remain queued.

Standalone Codex, Claude, OpenCode, and interactive shell clients are not
supported for authoritative automated input. They do not expose a native API
that also owns and atomically excludes their attached human composer, so they
fail closed. Codex app-server `turn/start` from a second client is likewise
insufficient: the server does not own unsent text in an attached stock TUI
composer.

This unsupported state is intentional product behavior. Adding a tool adapter
requires production evidence that its attached human client uses the same
arbiter and satisfies every step above; a terminal adapter cannot qualify.
