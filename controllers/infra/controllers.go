// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/vmware-tanzu/vm-operator/controllers/infra/capability"
	"github.com/vmware-tanzu/vm-operator/controllers/infra/configmap"
	"github.com/vmware-tanzu/vm-operator/controllers/infra/node"
	"github.com/vmware-tanzu/vm-operator/controllers/infra/secret"
	"github.com/vmware-tanzu/vm-operator/controllers/infra/validatingwebhookconfiguration"
	"github.com/vmware-tanzu/vm-operator/controllers/infra/workloadnetworkconfig"
	"github.com/vmware-tanzu/vm-operator/controllers/infra/zone"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
)

// AddToManager adds the controllers to the provided manager.
//
// In a multi-vCenter deployment, the capability and workload network
// controllers restart their own process, so every part runs them. The
// webhook configuration controller serves the webhooks, so it runs in the
// global process. The node, credential and zone controllers act on a vCenter
// client or the VM watcher, so they run in every shard.
//
// The wcp-cluster-config controller copies the vCenter PNID into the provider
// ConfigMap. Only the combined mode runs it: a shard takes its vCenter from its
// SDDC, and the global process uses the ConfigMap's vCenter-agnostic settings
// only.
func AddToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	multiVC := pkgcfg.FromContext(ctx).MultiVC
	runs := func(p multivc.Part) bool {
		return multivc.Runs(multiVC, p)
	}

	if err := capability.AddToManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to initialize infra capability controller: %w", err)
	}
	if multiVC.Mode == pkgcfg.MultiVCModeCombined {
		if err := configmap.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize infra configmap controller: %w", err)
		}
	}
	if runs(multivc.PartShard) {
		if err := node.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize infra node controller: %w", err)
		}
		if err := secret.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize infra secret controller: %w", err)
		}
	}
	if runs(multivc.PartGlobal) {
		if err := validatingwebhookconfiguration.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize validatingwebhookconfiguration webhook controller: %w", err)
		}
	}
	if pkgcfg.FromContext(ctx).Features.WorkloadNetworkConfiguration {
		if err := workloadnetworkconfig.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize workload network configuration controller: %w", err)
		}
	}
	if runs(multivc.PartShard) {
		if err := zone.AddToManager(ctx, mgr); err != nil {
			return fmt.Errorf("failed to initialize infra zone controller: %w", err)
		}
	}

	return nil
}
