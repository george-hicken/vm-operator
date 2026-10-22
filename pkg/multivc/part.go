// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// Part says which processes of a multi-vCenter deployment run a controller or
// service. The combined mode runs every part.
type Part int

const (
	// PartGlobal runs in the global process. It is for controllers that need
	// no vCenter client.
	PartGlobal Part = iota

	// PartShard runs in every shard. It is for controllers that realize a
	// resource in the shard's own vCenter.
	PartShard

	// PartSoleShard runs in a shard only while its SDDC is the only SDDC. It
	// is for controllers that publish vCenter-scoped data to a resource that
	// all shards would share, and that have no rule yet for which vCenter
	// writes it once several exist.
	PartSoleShard
)

// Runs returns true when the process runs the part.
func Runs(m pkgcfg.MultiVC, p Part) bool {
	switch m.Mode {
	case pkgcfg.MultiVCModeCombined:
		return true
	case pkgcfg.MultiVCModeGlobal:
		return p == PartGlobal
	case pkgcfg.MultiVCModeShard:
		return p == PartShard || (p == PartSoleShard && m.Sole)
	default:
		return false
	}
}
