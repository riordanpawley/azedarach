# go-bubbletea Docs Policy

This implementation does not maintain a separate phase-plan or feature-matrix document set.

## Source of Truth

- Normative behavior/specification: `../docs/spec/README.md`
- Functional requirements: `../docs/spec/04-functional-requirements.md`
- Acceptance catalog: `../docs/spec/06-acceptance-catalog.md`
- CLI JSON contract: `../docs/spec/12-az-cli-json-schema-and-examples.md`

## Local Implementation Notes

Any local documentation in `go-bubbletea/` must be:

- Non-normative
- Kept terminology-aligned with the spec (`issue`, `linear`, `az issue`)
- Updated in the same change as code that alters behavior

If a local doc conflicts with `docs/spec`, `docs/spec` is authoritative.
