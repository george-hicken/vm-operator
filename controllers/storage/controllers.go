// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/vmware-tanzu/vm-operator/controllers/storage/storageclass"
	"github.com/vmware-tanzu/vm-operator/controllers/storage/storagepolicy"
	"github.com/vmware-tanzu/vm-operator/controllers/storage/storagepolicyquota"
	"github.com/vmware-tanzu/vm-operator/controllers/storage/volumeattributesclass"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
)

// AddToManager adds the controllers to the provided manager.
//
// In a multi-vCenter deployment, the StoragePolicy controller publishes what
// one vCenter knows of a policy to a resource all shards share, so only the
// primary shard runs it. The other storage controllers need no vCenter client
// and run in the global process.
func AddToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	multiVC := pkgcfg.FromContext(ctx).MultiVC
	global := multivc.Runs(multiVC, multivc.PartGlobal)

	if global {
		if err := storagepolicyquota.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize storagepolicyquota controller: %w", err)
		}
	}

	features := pkgcfg.FromContext(ctx).Features

	if global && (features.BringYourOwnEncryptionKey || features.FastDeploy) {

		if err := storageclass.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize StorageClass controller: %w", err)
		}
	}

	if multivc.Runs(multiVC, multivc.PartSoleShard) &&
		(features.BringYourOwnEncryptionKey ||
			features.FastDeploy ||
			features.VMSharedDisks ||
			features.AllDisksArePVCs) {

		if err := storagepolicy.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize StoragePolicy controller: %w", err)
		}
	}

	if global && features.StoragePolicyMutability {
		if err := volumeattributesclass.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VolumeAttributesClass controller: %w", err)
		}
	}

	return nil
}
