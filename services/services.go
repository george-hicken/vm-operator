// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"sigs.k8s.io/controller-runtime/pkg/manager"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	vmwatcher "github.com/vmware-tanzu/vm-operator/services/vm-watcher"
)

// AddToManager adds all services to the provided manager.
func AddToManager(
	ctx *pkgctx.ControllerManagerContext,
	mgr manager.Manager) error {

	// The VM watcher watches a vCenter, so the global process, which has no
	// vCenter client, does not run it.
	if pkgcfg.FromContext(ctx).AsyncSignalEnabled &&
		multivc.Runs(pkgcfg.FromContext(ctx).MultiVC, multivc.PartShard) {
		if err := vmwatcher.AddToManager(ctx, mgr); err != nil {
			return err
		}
	}

	return nil
}
