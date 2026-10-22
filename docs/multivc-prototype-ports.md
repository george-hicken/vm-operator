# Multi-vCenter prototype shard ports

Each shard uses the host network. For a persisted port index `i` in `[0, 100)`, the manager binds health on `18000+i` and metrics on `18100+i`. The shard Deployment keeps the index in its `multivc.vmoperator.vmware.com/port-index` annotation, so adding or removing an SDDC does not renumber an existing shard. The Deployment uses the `Recreate` strategy so an old pod releases its ports before its replacement starts.

These fixed ranges repair a prototype scheduling collision: shard index 0 previously used health port 9460, which a stock TKG runtime-extension pod also requests as a host port. A captured Supervisor pod snapshot showed that pod requesting 9442, 9460, and 9876; another stock webhook requested 18312. The observed pod host ports and host-network command-line listeners had no overlap with 18000–18199. This is evidence for the captured installation, not a universal reservation of that range. Undeclared daemon listeners may exist.

Reassess the ranges if a new listener overlaps them, the 100-shard limit grows, multiple VM Operator installations share nodes, or numeric probes or sidecars need separate port handling. Port assignment remains deterministic; the renderer does not probe host availability or expose a new port setting.
