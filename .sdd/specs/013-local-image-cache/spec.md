# Local image cache bootstrap

Status: Draft

## Behavior

A VM assigned to a vCenter shard resolves its image to a unique local content-library item. With Fast Deploy enabled, that shard initializes a hardware-cache prerequisite for the local item before waiting for cached hardware. The local item ID and content version determine the cache identity. The original image and its source cache retain their identities.

A VM already waiting for an absent cache can recover the prerequisite without entering a VM create or update operation. A waiting label inconsistent with the resolved image is rejected. The existing cache controller remains responsible for hardware and file readiness, and its existing watch wakes waiting VMs.

## Acceptance criteria

- Independent vCenters with distinct item IDs and source-only image metadata bootstrap the owning vCenter's cache.
- Missing, ambiguous or incompatible local items do not produce a falsely ready cache.
- Repeated bootstrap preserves readiness, existing locations and unchanged resource versions.
- Recovery preserves the early VM readiness guard, including when a VM already exists.
- Other shards leave foreign cache items alone; combined and sole-shard behavior is preserved.

## Scope

No API schema, RBAC, namespace, feature switch, placement policy or image metadata portability assumption changes. This repairs the existing multi-vCenter and Fast Deploy feature combination.
