# Team Collaboration Architecture

## Status

This document records the accepted product architecture for the first Azedarach
team-collaboration release. It is a research artifact, not an implementation
plan or authorization to begin the migration.

The intended audience is one small, trusted team of developers collaborating on
one Git repository. The product goal is shared semantic context for people and
the agents they run locally. It is not remote agent orchestration.

The durable decisions are `dec-10` through `dec-12` and `dec-16` through
`dec-27`. Detailed event-store, projection, replay, signing, schema-evolution,
workflow-runtime, and recovery mechanics remain owned by investigation `dgv`.

## Product Boundary

One collaboration deployment is authoritative for exactly one Azedarach project,
and one project maps permanently to exactly one Git repository. A developer may
connect their local Azedarach registry to several independently deployed projects,
but there is no shared server-side workspace of projects in the first release.

The first release synchronizes:

- tickets, lifecycle, containment and blocking relationships, comments, and
  human interactions;
- specs, requirements, links, and verification evidence;
- decisions, rationale, supersession, and related learnings;
- compact semantic agent continuation and validation evidence;
- deliberately published artifact metadata and bytes;
- project membership, device authorization, and attribution;
- repository, commit, branch, and pull-request references;
- GitHub review, check, and merge observations admitted through the GitHub App.

The first release does not synchronize or control:

- agent processes, tmux sessions, worktrees, dev servers, or local orchestration;
- runner presence, execution leases, attempt fencing, or remote agent startup;
- terminal bytes, raw prompt/response transcripts, tool-call streams, heartbeats,
  activity samples, or noisy telemetry;
- local Git status, uncommitted changes, local absolute paths, environment
  snapshots, or secrets;
- arbitrary files that a user did not deliberately publish.

Each developer continues to start agents and run Azedarach orchestration on their
own machine. A local agent may read and update shared semantic records through
the local Az client, but the project service does not observe, schedule, or
control that agent.

## Topology

```text
developer machine A                         developer machine B
-------------------                         -------------------
local agents / tmux / Git                   local agents / tmux / Git
          |                                           |
local az daemon + SQLite projection         local az daemon + SQLite projection
          | pending content outbox                    | pending content outbox
          +--------------------+----------------------+
                               |
                    authenticated HTTPS stream
                               |
                 one Go project-service process
                 - membership and authorization
                 - semantic command validation
                 - one canonical project order
                 - snapshots and incremental sync
                 - GitHub provider integration
                               |
                  +------------+-------------+
                  |                          |
          one active SQLite WAL       artifact byte volume
          project authority           content-addressed
```

The project service owns shared semantic intent and ordering. It has no repository
checkout and runs no `git clone`, `fetch`, `push`, `merge`, `rebase`, hooks, or
worktree operations. Developers use their existing local clones and Git
credentials. The GitHub App operates through provider APIs and therefore does
not turn the service into a Git runner.

## Authority And Local-First Contract

Every client keeps a complete local SQLite projection for fast reads. The active
project service is the only shared command authority and uses one SQLite WAL
database with serialized writes. Clients do not merge databases or become peer
authorities.

Commands fall into two product classes:

| Class | Examples | Offline behavior |
| --- | --- | --- |
| Content | Draft or edit ticket text, a spec, requirement, decision, learning, comment, or evidence description | Queue durably and render as visibly pending |
| Guarded | Lifecycle transition, relationship mutation, review acceptance, integration, membership, project policy, retirement | Require a current online authority decision |

An optimistic content change is never presented as canonical before acceptance.
On reconnect the client first refreshes canonical state, rebases pending commands,
submits them in causal order, and reports which were accepted, transformed,
conflicted, or rejected. A failure blocks only causally dependent pending work,
not the entire outbox.

Disjoint field patches coexist. Accepted same-field patches are ordered by the
project authority; superseded values remain inspectable and restorable, and the
affected editor receives a notice. Structured edits that cannot safely rebase
return a typed conflict with edit-and-retry, discard, and restore-as-draft paths.

Guarded commands are disabled when the client is offline, has a projection gap,
or is rebuilding. The UI must expose connection state, source freshness, and
pending count so cached availability never masquerades as current authority.

## Identities And Lifecycles

- The project has a stable authority identity independent of its deployment URL.
- Client-creatable semantic objects use immutable UUIDv7 identities.
- Short ticket keys are human-facing aliases assigned by the project authority.
- A member is a durable project identity; an invitation is not membership.
- Each enrolled device has its own identity and public key under one member.
- Accepted records retain original actor and device attribution after revocation.
- Archive changes visibility, not identity or semantic history.
- Retirement seals an authority read-only; identities are never reused.

## Roles And Authorization

The first release has two roles:

| Capability | Member | Owner |
| --- | --- | --- |
| Read shared semantic records | yes | yes |
| Create and edit ordinary semantic content | yes | yes |
| Run guarded ticket commands | yes | yes |
| Participate in review and GitHub integration | yes, subject to repository policy | yes |
| Invite or remove members and revoke another member's devices | no | yes |
| Change project-wide policy | no | yes |
| Transfer ownership, recover, or retire the project | no | yes |
| Use exceptional override | no | yes, explicitly and audibly |

A project must always have at least one owner. Ownership transfer is explicit.
Removing a member prevents new commands, revokes invitations, device credentials,
and provider credentials, and fences any in-flight authorization. It does not
erase previously accepted records or attribution.

Agents do not become project members in the first release. An agent acts through
the invoking member's local Az client. Accepted commands distinguish human and
local-agent origin and may carry a stable agent invocation identifier as
provenance under the accountable member and device. That identifier grants no
membership, presence, lease, or execution authority.

## Authentication And Device Enrollment

Human authentication uses provider-neutral passkeys. The initial owner receives
offline recovery codes. GitHub identity is linked separately and is not the root
of Azedarach membership.

The normal join flow is:

1. An owner creates a single-use invitation bound to this project and the
   `member` role. It expires after seven days and may be revoked before use.
2. The invitee opens the project URL, verifies the project identity, and creates
   or uses a passkey.
3. Invitation consumption creates the member identity.
4. The CLI presents a short browser enrollment flow for a named device key.
5. The member approves that device and receives the initial project snapshot.
6. Incremental synchronization resumes from the snapshot cursor.

Invitation consumption and device enrollment are deliberately separate. A
stolen invitation cannot silently authorize an arbitrary long-lived machine.

Revoking a device immediately rejects new commands and pending uploads signed by
that device. Already accepted project history remains valid and local cached
data may remain readable; the product must not claim remote erasure of bytes it
does not control.

## Git And GitHub Boundary

Local Git remains local:

- developers clone, fetch, edit, commit, resolve conflicts, rebase, and push;
- developer credentials remain in their normal local Git credential store;
- Az may invoke local Git through existing local workflows, but the collaboration
  service neither receives those credentials nor maintains a checkout;
- shared records reference exact repository, branch, commit, and pull-request
  identities rather than machine-specific paths.

The published GitHub App provides the shared provider lifecycle:

- connect the one configured repository;
- link tickets and review evidence to exact commits and PRs;
- create the final root PR as a draft at the first verified root SHA;
- update shared PR state, checks, reviews, and expected head SHA;
- mark the root PR ready when Az review handoff occurs;
- reconcile webhook delivery with provider polling;
- perform guarded merge through the GitHub API when repository rules pass.

Repository branch protection remains the ultimate merge gate. Merge method is a
project setting rather than hardcoded. Before creating, updating, or merging a
PR, Az verifies that the provider head is the expected SHA. Unexpected branch
movement makes the operation stale and requires reconciliation.

The official relay holds the published App identity and handles OAuth/token
exchange and webhook routing. A bring-your-own private App implements the same
contract directly. The relay is not a project authority and does not receive
semantic history or repository contents. Project deployments keep linked-user
credentials in encrypted secret storage rather than semantic events.

Webhook deliveries are deduplicated and may arrive late, duplicated, reordered,
or not at all. Polling repairs gaps. After an uncertain PR creation or merge
result, Az queries provider state before retrying and never blindly repeats an
irreversible operation.

## Artifact Publication

Artifact publication is an explicit user or agent action. Metadata is permanent
shared semantic context and includes a content hash, media type, size, display
name, publishing actor, creation time, and the record it supports. Bytes live in
content-addressed storage outside SQLite and download into bounded local caches
on demand.

The project backup includes both the semantic database and published artifact
bytes. If bytes are temporarily missing, metadata remains visible with a clear
`content unavailable` state and a recovery action. Missing payloads must not
silently disappear from evidence lists.

Raw transcripts, terminal output, secrets, environment snapshots, and arbitrary
local files are never published automatically. Redaction and selective erasure
mechanics are deferred; the first release relies on deliberate admission and
permanent accepted history.

## Behavior Packets For EventStorming

These packets state product meaning without choosing aggregates, logical streams,
event envelopes, projector topology, database schema, or replay mechanics.

### Packet A: Publish A Solo Project

**Actors and goal:** A project owner wants to turn one existing solo project into
the canonical shared project without fabricating or merging history.

**Happy path:**

1. The owner requests a publication preview.
2. Az classifies current semantic records, local-only runtime material, secrets,
   paths, and deliberately published artifacts.
3. The owner reviews imported and excluded classes and confirms this database as
   the sole source.
4. The local actor is bound to the initial owner identity.
5. The collaborative authority is established with current semantic state and
   available provenance.
6. The deployment reports the collaboration cutover and becomes available for
   invitations.

**Refusals and recovery:** Publication refuses an unclassified secret, ambiguous
repository identity, invalid source database, or already-active collaborative
authority. Failure before activation is retryable. Failure after authority
creation enters explicit recovery; it must not create a second active lineage.

**Invariants:** One selected database only; one repository identity; no invented
pre-cutover chronology; no automatic merge of other local databases.

**Read model:** Preview shows counts and examples by imported/excluded class,
source fingerprint, repository identity, initial owner, artifact availability,
and backup readiness.

### Packet B: Invite, Join, Enroll, Revoke

**Actors and goal:** An owner authorizes a teammate; the teammate authorizes one
machine; either the member or owner later removes a compromised machine.

**Happy path:** Invitation issued, invitation consumed, member joined, device
enrollment requested, device approved, snapshot installed, and synchronization
caught up.

**Concurrent and stale cases:** Two consumers of one invitation race; only one
may join. A revoked or expired invitation refuses consumption. Revocation racing
with a command is decided by the authority order. A removed member cannot enroll
a new device with an old browser session.

**Recovery:** Lost passkeys use owner-approved recovery and the offline recovery
material. Lost device keys require a fresh device enrollment, not identity reuse.

**Invariants:** At least one owner; invitation is single-use; devices are
independently revocable; revocation does not rewrite accepted attribution.

**Read model:** Membership shows role, active/revoked devices, last accepted
activity time, pending invitations, and security-relevant changes without
exposing credentials.

### Packet C: Edit Shared Semantic Content Offline

**Actors and goal:** A member or local agent continues useful content work during
a network interruption and later reconciles it safely.

**Happy path:** A content command is stored in the local outbox, its optimistic
effect appears as pending, connectivity returns, canonical state refreshes, the
command is rebased and accepted, and the pending marker clears.

**Conflict path:** Another member changes the same field first. The later patch is
ordered and the superseded value remains restorable, or a structured stale edit
is refused with edit-and-retry choices. Disjoint changes coexist.

**Dependency path:** A pending comment referring to a pending ticket waits for the
ticket identity to be accepted. Rejection of that ticket blocks the dependent
comment but not independent decisions or specs in the same outbox.

**Invariants:** Guarded commands never queue offline; optimistic state is labeled;
duplicate submission returns the original result; reconnect does not submit
against an unrefreshed projection.

**Read model:** Every affected record exposes canonical state, pending overlays,
conflict/rejection state, source freshness, and available recovery actions.

### Packet D: Change Ticket Lifecycle Or Relationships

**Actors and goal:** A member changes shared work state while preserving graph,
completion, and review invariants.

**Happy path:** The online client sends the guarded intent with its known record
revision; authority evaluates current lifecycle and relationships; the change is
accepted; every client updates readiness and board projections.

**Strict rules:** Containment and blocking are distinct. Parent completion waits
for live children. A derived wait-for deadlock is rejected with an explanatory
path. Attaching a live child to a closed parent never silently reopens it.

**Compound intent:** `reopen and attach child` is explicit. The user sees pending,
partial, retryable, or intervention state if the underlying cross-domain process
does not finish. Product UX must not claim atomic success merely because one
part completed.

**Concurrent cases:** Two relationship mutations that jointly introduce a cycle,
close racing with a new child, reparent racing with child completion, and stale
review acceptance all receive current authoritative evaluation rather than local
last-write-wins.

**Read model:** Ticket detail explains current lifecycle, parent, children,
blockers, derived readiness/waits, review generation, and any incomplete guarded
workflow.

### Packet E: Deliver Through GitHub

**Actors and goal:** Members push code locally while Az provides one shared view
and guarded lifecycle for the root PR.

**Happy path:** A member pushes an exact SHA, Az verifies it is reachable, creates
or updates the draft root PR, links evidence, observes checks and reviews, marks
the PR ready at review handoff, verifies the expected head and repository rules,
and submits the configured provider merge.

**Uncertain effects:** Duplicate webhooks are ignored by provider identity.
Missing webhooks are repaired by polling. A timeout after PR creation or merge
causes provider reconciliation before retry. Checks for an older head are stale.

**Drift:** Unexpected head movement, base advancement, missing commit, revoked
GitHub authorization, or changed repository installation pauses the operation
with a specific recovery path. Az never substitutes another SHA silently.

**Invariants:** The service has no checkout; local credentials perform pushes;
one root PR is canonical; review and checks are tied to the expected head; branch
protection remains authoritative.

**Read model:** Ticket/root view shows repository, PR, expected and observed head,
checks, reviews, provider freshness, merge eligibility, and degraded provider
state.

### Packet F: Publish And Recover An Artifact

**Actors and goal:** A member deliberately shares evidence that is too large or
unsuitable for semantic SQLite records.

**Happy path:** The client classifies and hashes the file, the user confirms
publication, bytes upload idempotently, metadata is accepted, and other clients
download/cache bytes on demand.

**Failures:** Duplicate upload reuses the content hash. Interrupted upload resumes
or retries. Metadata never claims availability before the service verifies the
bytes. Missing backup payload appears as unavailable and is recoverable without
rewriting its semantic reference.

**Invariants:** No automatic transcript or arbitrary-directory admission; no
secret-bearing payload; metadata is permanent; byte storage and backup are part
of the same supported project recovery contract.

### Packet G: Back Up, Restore, Upgrade, And Degrade

**Actors and goal:** A self-hosting owner keeps the project recoverable without
creating two writers.

**Happy path:** Automated backup captures a consistent semantic database,
artifacts, authority/recovery metadata, and a manifest; off-machine custody is
confirmed; a restore drill verifies a new empty deployment; upgrades take a
pre-upgrade snapshot, enter maintenance, migrate, verify, and reopen.

**Failures:** Disk/WAL pressure, stale backup, artifact mismatch, migration
failure, version incompatibility, or unavailable provider produces a visible
degraded or maintenance state. Cached reads and offline content drafts remain
available where safe; guarded commands do not weaken their rules.

**Fork prevention:** Restoring a backup does not silently authorize two active
copies. The operator must explicitly continue or establish the valid authority
lineage according to the recovery design owned by `dgv`.

**Read model:** Health exposes service readiness, database/WAL pressure, disk,
backup age, last restore drill, artifact integrity, synchronization lag, and
authentication/provider failures without logging semantic payloads or secrets.

### Packet H: Retire A Project

**Actors and goal:** An owner ends normal collaboration while preserving an
inspectable and recoverable record.

**Happy path:** Retirement preview identifies active members, pending commands,
provider linkage, backup health, and published artifacts; owner confirms;
authority stops accepting mutations; credentials are revoked; final export is
produced; clients become read-only.

**Refusals:** Retirement refuses while required recovery material is missing or
another owner-transfer/recovery operation is unresolved. Exceptional override is
owner-only, explicit, and separately auditable.

**Invariants:** Retirement is not deletion; history is not rewritten; identity is
not reused. Physical storage removal is a later explicit operator action outside
normal semantic commands.

## Deployment Defaults

The supported first-run deployment is Docker Compose containing:

- one Go project-service process;
- one durable SQLite WAL volume;
- one durable content-addressed artifact volume;
- an optional Caddy profile for certificate provisioning and TLS termination.

HTTPS is mandatory outside loopback. The database is never exposed to clients.
One service instance is the only writer; multi-instance high availability is not
claimed. PostgreSQL should be reconsidered only when the product needs
multi-instance HA, consolidated multi-project hosting, or measurements show
sustained contention beyond the serialized SQLite writer.

Operational defaults must include automated off-machine backups, daily retained
recovery points, more frequent database capture appropriate to the configured
recovery objective, pre-upgrade snapshots, restore verification, bounded local
artifact caches, health endpoints, and structured logs that exclude semantic
payloads and secrets.

## DGV/DHQ Reconciliation Boundary

DDA owns the product policies above. DGV owns how they are represented and
recovered through semantic event sourcing. In particular, this document does
not choose:

- aggregate or bounded-context boundaries;
- logical stream identities or compound transaction exceptions;
- event envelopes, signing keys, hashes, sequence allocation, or command-receipt
  schema;
- projector topology, checkpoints, snapshots, rebuilds, or upcasters;
- workflow reducer/runtime design, timers, compensations, or replay mechanics;
- authority fork, key-loss, or recovery cryptographic implementation.

DDA is the required upstream product-policy input and closes before DGV. DGV
therefore has a blocking dependency on DDA. Final product-to-mechanics
traceability is owned by DGV child `did` and is completed before DGV acceptance;
it is not a gate on DDA integration.

The accepted product policies align with current DGV/DHQ findings as follows:

| Product policy | EventStorm/DGV consequence |
| --- | --- |
| One project authority and one repository | One project authority boundary; user and device authority remain distinct |
| Offline content only | Pending local overlays are not canonical events; guarded commands require authority |
| Permanent semantic history | Corrections and supersession append new meaning rather than rewrite history |
| Online lifecycle/graph invariants | Strict current decision policy; projections cannot become hidden authority |
| Explicit reopen-and-attach | Cross-domain partial progress must be visible; no silent side effect |
| Provider API integration | GitHub is an external effect/observation source with uncertainty and reconciliation |
| No runtime collaboration | Session, runner, lease, and orchestration collaboration processes are outside v1 |
| Deliberate artifact publication | Permanent semantic metadata is separate from byte payload storage and admission |
| Project sealing | Retirement is a semantic terminal policy distinct from physical deletion |

Current DGV decisions `dec-13` through `dec-15` remain compatible: one signed
semantic sequence per authority, derived projections, and version-pinned durable
workflows with explicit takeover/handoff. The unresolved mechanics question is
how compound user intents such as reopen-and-attach are divided across streams
or workflows; the product contract is fixed regardless: no silent reopen, no
false atomic-success claim, and visible retry or intervention.

## Deferred Possibilities, Not Commitments

The following may be investigated later but are not implied roadmap promises:

- runner enrollment, device capabilities, and execution presence;
- cross-machine agent orchestration or remote attach;
- shared, hosted, or ephemeral runners;
- execution leases, fenced attempts, and server-directed Git operations;
- a multi-project team workspace or hosted organization control plane;
- enterprise roles, SSO/SCIM, quotas, billing, or regional placement;
- payload redaction and selective erasure beyond deliberate admission controls.

## Research Acceptance Checklist

- [x] One deployment/project/repository boundary is explicit.
- [x] Small-team shared semantic surface and exclusions are explicit.
- [x] Local execution and Git boundaries are explicit.
- [x] Authentication, invitations, devices, revocation, and roles are explicit.
- [x] Offline, pending, conflict, reconnect, and degraded behavior are explicit.
- [x] Ticket lifecycle and relationship invariants are explicit.
- [x] GitHub App scope and uncertain provider behavior are explicit.
- [x] Artifact admission, storage, backup, and missing-byte behavior are explicit.
- [x] Solo publication, deployment, backup, restore, upgrade, and retirement are explicit.
- [x] EventStorm-ready actors, timelines, invariants, failures, recovery, and read models are present.
- [x] DDA product policy is separated from DGV event-source mechanics.
- [x] Human accepted the complete DDA product-policy recommendation set.
- [x] DDA is the blocking upstream input to DGV, not the downstream consumer.
- [ ] DGV child `did` records final product-to-mechanics traceability before DGV closes.
