// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// AddReadinessCheck registers the readiness probe for the process mode.
// Shards have no webhook server; registering their check independently also
// makes controller-runtime serve the inherited Deployment's /readyz probe.
func AddReadinessCheck(mgr ctrlmgr.Manager, mode pkgcfg.MultiVC) error {
	name, check := "ping", healthz.Ping
	if mode.RunsWebhooks() {
		name, check = "webhook", mgr.GetWebhookServer().StartedChecker()
	}
	if err := mgr.AddReadyzCheck(name, check); err != nil {
		return fmt.Errorf("failed to register %s readiness check: %w", name, err)
	}
	return nil
}
