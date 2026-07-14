# dec-19: Use invitation-backed passkeys and independently revocable devices

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Authenticate people with provider-neutral passkeys, give owners offline recovery codes, use single-use expiring project invitations, and enroll each CLI/device under a separately named public key that can be revoked without deleting the member.

## Context

One small self-hosted team needs secure membership without passwords, email infrastructure, or GitHub becoming the root identity provider.

## Consequences

Invitation consumption creates membership but not automatic device trust. Device revocation rejects new commands and pending uploads from that device while already accepted history and local cached bytes remain attributable and readable.

## Links

- applies-to issue:dda
