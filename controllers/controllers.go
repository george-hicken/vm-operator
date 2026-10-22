// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/vmware-tanzu/vm-operator/controllers/configtarget"
	"github.com/vmware-tanzu/vm-operator/controllers/contentlibrary"
	"github.com/vmware-tanzu/vm-operator/controllers/infra"
	"github.com/vmware-tanzu/vm-operator/controllers/multivcshard"
	"github.com/vmware-tanzu/vm-operator/controllers/storage"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachine"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachineclass"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachineconfigoptions"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinegroup"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinegrouppublishrequest"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachineimagecache"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinepublishrequest"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinereplicaset"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachineservice"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinesetresourcepolicy"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinesnapshot"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinewebconsolerequest"
	"github.com/vmware-tanzu/vm-operator/controllers/vspherepolicy/policyevaluation"
	"github.com/vmware-tanzu/vm-operator/controllers/vspherepolicy/tag"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
)

// AddToManager adds all controllers to the provided manager.
//
// In a multi-vCenter deployment each controller runs in one part, which
// multivc.Runs decides: the controllers that need no vCenter client run in
// the global process, and the controllers that realize resources in a vCenter
// run in every shard. The controllers that publish vCenter-scoped data to
// shared resources, and have no rule yet for which vCenter writes it, run
// only while the Supervisor has one SDDC. The combined mode runs all of them,
// as before.
func AddToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	config := pkgcfg.FromContext(ctx)
	runs := func(p multivc.Part) bool {
		return multivc.Runs(config.MultiVC, p)
	}

	// Each shard syncs the library items that are in its own vCenter.
	if runs(multivc.PartShard) {
		if err := contentlibrary.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize ContentLibrary controllers: %w", err)
		}
	}
	if err := infra.AddToManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to initialize Infra controllers: %w", err)
	}
	if err := storage.AddToManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to initialize storage controllers: %w", err)
	}
	if err := virtualmachine.AddToManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to initialize VirtualMachine controller: %w", err)
	}
	if runs(multivc.PartGlobal) {
		if err := virtualmachineclass.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineClass controller: %w", err)
		}
		if err := virtualmachineservice.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineService controller: %w", err)
		}
	}
	if runs(multivc.PartShard) {
		if err := virtualmachinesetresourcepolicy.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineSetResourcePolicy controller: %w", err)
		}
		if err := virtualmachinewebconsolerequest.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineWebConsoleRequest controller: %w", err)
		}
		if err := virtualmachinepublishrequest.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachinePublishRequest controller: %w", err)
		}
	}

	if err := addFeatureControllers(ctx, mgr, config, runs); err != nil {
		return err
	}

	if config.MultiVC.IsGlobal() {
		if err := multivcshard.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize multi-vCenter shard controller: %w", err)
		}
		if err := multivcshard.AddPlacementToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize multi-vCenter placement controller: %w", err)
		}
	}

	return nil
}

// addFeatureControllers adds the controllers that a feature gates, each in its
// multi-vCenter part.
func addFeatureControllers(
	ctx *pkgctx.ControllerManagerContext,
	mgr manager.Manager,
	config pkgcfg.Config,
	runs func(multivc.Part) bool) error {

	if config.Features.K8sWorkloadMgmtAPI && runs(multivc.PartGlobal) {
		if err := virtualmachinereplicaset.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineReplicaSet controller: %w", err)
		}
	}

	if config.Features.FastDeploy && runs(multivc.PartShard) {
		if err := virtualmachineimagecache.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VMI controllers: %w", err)
		}
	}

	if config.Features.VMSnapshots && runs(multivc.PartShard) {
		if err := virtualmachinesnapshot.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineSnapshot controller: %w", err)
		}
	}

	// A VirtualMachineGroup can hold VMs of several shards. Groups are not
	// split by vCenter yet, so they are placed only while there is one SDDC.
	if config.Features.VMGroups && runs(multivc.PartSoleShard) {
		if err := virtualmachinegroup.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VMG controller: %w", err)
		}
		if err := virtualmachinegrouppublishrequest.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineGroupPublishRequest controller: %w", err)
		}
	}

	if config.Features.VSpherePolicies && runs(multivc.PartGlobal) {
		if err := policyevaluation.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize vSphere policy evaluation controller: %w", err)
		}
	}

	if config.Features.TaggingAPI && runs(multivc.PartGlobal) {
		if err := tag.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize vSphere Policy Tag controllers: %w", err)
		}
	}

	if config.Features.VirtualMachineConfigPolicy && runs(multivc.PartSoleShard) {
		// ConfigTarget fans out VirtualMachineConfigOptions, so it's
		// registered first.
		if err := configtarget.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize ConfigTarget controller: %w", err)
		}
		if err := virtualmachineconfigoptions.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize VirtualMachineConfigOptions controller: %w", err)
		}
	}

	return nil
}
