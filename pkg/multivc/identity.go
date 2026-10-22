// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"errors"
	"fmt"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// ErrInvalidMode is returned for a VMOP_MULTIVC_MODE value that is not a known
// mode, or a shard without an SDDC.
var ErrInvalidMode = errors.New("invalid multi-vCenter mode")

// ValidateIdentity returns an error when the multi-vCenter configuration
// cannot be run: an unknown mode, or a shard that names no SDDC.
func ValidateIdentity(m pkgcfg.MultiVC) error {
	switch m.Mode {
	case pkgcfg.MultiVCModeCombined, pkgcfg.MultiVCModeGlobal:
		if m.ShardSDDC != "" {
			return fmt.Errorf("%w: VMOP_SHARD_SDDC is set outside shard mode", ErrInvalidMode)
		}
		return nil
	case pkgcfg.MultiVCModeShard:
		if m.ShardSDDC == "" {
			return fmt.Errorf("%w: shard mode requires VMOP_SHARD_SDDC", ErrInvalidMode)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidMode, m.Mode)
	}
}

// LeaderElectionID returns the leader election ID for the process: the base
// ID in combined mode, and the base ID with a "-global" or "-<sddc>" suffix
// otherwise, so the global process and each shard elect a leader
// independently.
func LeaderElectionID(base string, m pkgcfg.MultiVC) string {
	switch m.Mode {
	case pkgcfg.MultiVCModeGlobal:
		return base + "-global"
	case pkgcfg.MultiVCModeShard:
		return base + "-" + m.ShardSDDC
	default:
		return base
	}
}
