# dec-28: Separate shared project, device-root, and device-local semantic authorities

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Use independent authority scopes that match ownership and lifecycle. Shared project facts belong to the project authority. Device-global configuration and project-registry facts belong to a device-local root authority. Material local workflow intent and outcomes belong to device-local semantic authority, while raw telemetry remains ephemeral. Local facts enter shared project history only through explicit guarded semantic publication. Connect authorities through stable identities and causal references rather than a synthetic global total order.

## Context

Collaboration V1 shares semantic project context but deliberately does not synchronize or control agents, sessions, tmux, worktrees, dev servers, local orchestration, or other device runtime. Azedarach still intends to event-source material whole-app durable behavior without leaking local operational history into team projects or inventing a hosted user workspace.

## Consequences

Each authority has its own signed canonical ordering. The cross-project database remains a derived local view. Portable multi-device user authority is deferred. Local runtime history is never automatically replicated. Publishing evidence, review packets, decisions, checkpoints, or artifacts creates explicit shared facts with provenance. Aggregate and logical stream boundaries remain open for EventStorm discovery.

## Links

- applies-to issue:dgv
- applies-to issue:dhq
