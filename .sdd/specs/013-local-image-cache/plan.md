# Implementation plan

Extract the existing provider's image-cache CreateOrPatch into a shared private helper. Call it after local item resolution and validation, before config generation reads the cache. Require a waiting label to match the resolved cache name.

Expose a provider prerequisite-recovery operation that only resolves the image and initializes its cache. When the VM controller's early guard finds a missing cache in a multi-vCenter shard, invoke that operation and continue waiting. This avoids entering the normal provider operation, which can discover an existing VM and take its update path.

Preserve provider ID/version naming, controller ownership checks, cache event mapping and later datastore-location population. The existing cache controller sets all readiness conditions. No dependencies or generated APIs change.

Validate with independent simulator catalogs, provider creation and recovery tests, controller guard tests and envtest hardware/watch coverage. Run affected provider and controller suites, configured lint, root build and vet. The external multi-vCenter end-to-end scenario is a same-effort acceptance gate; repository-native multi-vCenter E2E coverage remains follow-up.
