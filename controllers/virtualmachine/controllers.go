// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package virtualmachine

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachine/storagepolicyusage"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachine/virtualmachine"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachine/volume"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachine/volumebatch"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
)

// AddToManager adds the controllers to the provided manager.
// AddToManager adds the controllers to the provided manager.
//
// In a multi-vCenter deployment, the VirtualMachine and volume controllers
// realize VMs in a vCenter, so they run in every shard, each for the VMs in
// its cache. The StoragePolicyUsage controller sums usage over all VMs and
// needs no vCenter client, so it runs in the global process.
func AddToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	multiVC := pkgcfg.FromContext(ctx).MultiVC

	if multivc.Runs(multiVC, multivc.PartGlobal) {
		if err := storagepolicyusage.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize virtualmachine storagepolicyusage controller: %w", err)
		}
	}

	if !multivc.Runs(multiVC, multivc.PartShard) {
		return nil
	}

	if err := virtualmachine.AddToManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to initialize virtualmachine controller: %w", err)
	}

	if pkgcfg.FromContext(ctx).Features.VMSharedDisks {
		if err := volumebatch.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize Volume Batch controller: %w", err)
		}
	} else {
		if err := volume.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize virtualmachine volume controller: %w", err)
		}
	}

	return nil
}
