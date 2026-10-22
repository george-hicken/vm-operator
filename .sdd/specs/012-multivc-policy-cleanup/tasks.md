# Tasks: multi-vCenter resource policy deletion fence

- [T001] [vmop-44836] Persist the shared gate before shard provider create/update using fresh optimistic metadata (`controllers/virtualmachinesetresourcepolicy/virtualmachinesetresourcepolicy_controller.go`).
- [T002] [vmop-44836] Fence shard deletion before provider cleanup and preserve the shared gate (`controllers/virtualmachinesetresourcepolicy/virtualmachinesetresourcepolicy_controller.go`).
- [T003] [vmop-44836] Verify persisted gate, metadata conflict, deletion race, and combined regression (`controllers/virtualmachinesetresourcepolicy/virtualmachinesetresourcepolicy_controller_test.go`).
- [T004] [vmop-44836] Establish complete historical obligations and a durable per-SDDC deletion protocol before enabling shard policy deletion (future change).
