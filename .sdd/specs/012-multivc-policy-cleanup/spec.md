# Multi-vCenter resource policy deletion fence

**Status**: Draft
**Branch**: `topic/gh012934/codex-vmop-policy-cleanup`
**Epic**: TBD

## Goals

- A shard MUST persist the shared deletion gate before it creates any resource policy object in vCenter.
- A shard MUST NOT remove the shared gate when its local cleanup cannot prove that all SDDCs are complete.
- Combined mode MUST retain its existing creation and deletion behavior.

## Non-goals

- Automatic resource policy deletion in shard mode. It remains blocked until historical cleanup obligations can be proven and a deletion protocol is implemented.
- Automatic retirement of a missing SDDC's obligations.

## Acceptance criteria

- A shard's provider sees the shared finalizer already persisted on the policy before creation or update.
- A policy deletion in shard mode returns an explicit unsupported error, does not call provider cleanup, and does not remove the shared finalizer, even when status arrays are empty.
- A concurrent metadata update does not silently drop the shared finalizer.
- Combined mode continues to remove its shared finalizer after successful provider cleanup.

## Open questions

- [NEEDS CLARIFICATION: What authoritative inventory can attest every historical SDDC for a legacy policy with folder-only resources?]
