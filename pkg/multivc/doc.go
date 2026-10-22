// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

// Package multivc holds what VM Operator needs to run as one global process
// and one shard per SDDC when a Supervisor spans several vCenters.
//
// It is a prototype. Its parts map one to one onto the parts of a planned
// shared sharding library, so that adopting the library later replaces this
// package rather than its callers:
//
//   - identity: the mode a process runs in and the SDDC a shard serves.
//   - membership: the SDDCs of the Supervisor, read from their SDDC
//     resources, and which of them is primary.
//   - resolver: the SDDC a zone belongs to, through the owner reference of
//     its AvailabilityZone.
//   - selector: the label selector that limits a shard's VM cache to the VMs
//     in its zones.
//   - client provider: the vCenter endpoint, trust and identity of a shard's
//     SDDC.
//
// No VM, zone or dependent resource carries a shard or vCenter label. A VM is
// associated with an SDDC through its zone label, and dependent resources
// through their VM.
package multivc
