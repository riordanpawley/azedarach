# dec-27: Publish selected artifacts by content address outside SQLite

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Make artifact publication deliberate. Synchronize permanent content-addressed metadata, store bytes outside SQLite, download and cache them on demand, and back them up with the project.

## Context

Teams need shared screenshots, reports, and selected evidence, but automatic payload capture would recreate transcript weight, sensitivity, and storage problems.

## Consequences

Never automatically publish transcripts, terminal output, secrets, or arbitrary local files. Missing bytes remain visibly unavailable without corrupting or hiding the semantic metadata that references them.

## Links

- applies-to issue:dda
