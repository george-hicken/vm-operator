// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/retry"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// PatchResourcePolicyStatus persists this shard's resource pool and cluster
// module entries, preserving the latest entries of all other shards. Conflicts
// retry only the status write, so modules already created in vCenter are reused
// rather than being recreated by another provider reconciliation.
// The reader must bypass the controller cache to observe conflicting writes.
func PatchResourcePolicyStatus(
	ctx context.Context,
	c ctrlclient.Client,
	reader ctrlclient.Reader,
	before, after *vmopv1.VirtualMachineSetResourcePolicy) error {

	mode := pkgcfg.FromContext(ctx).MultiVC
	if !mode.IsShard() {
		return fmt.Errorf("resource policy shard status patch requires shard mode")
	}
	if apiequality.Semantic.DeepEqual(before.Status, after.Status) {
		return nil
	}
	owns := func(key string) bool { return strings.HasPrefix(key, mode.ShardSDDC+"/") }

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &vmopv1.VirtualMachineSetResourcePolicy{}
		if err := reader.Get(ctx, ctrlclient.ObjectKeyFromObject(after), latest); err != nil {
			return err
		}
		if latest.UID != before.UID {
			return fmt.Errorf("resource policy was replaced while reconciling")
		}
		base := latest.DeepCopy()
		latest.Status.ResourcePools = slices.DeleteFunc(latest.Status.ResourcePools,
			func(entry vmopv1.ResourcePoolStatus) bool { return owns(entry.ClusterMoID) })
		for _, entry := range after.Status.ResourcePools {
			if owns(entry.ClusterMoID) {
				latest.Status.ResourcePools = append(latest.Status.ResourcePools, entry)
			}
		}
		latest.Status.ClusterModules = slices.DeleteFunc(latest.Status.ClusterModules,
			func(entry vmopv1.VSphereClusterModuleStatus) bool {
				// A legacy entry without a cluster key may have been claimed
				// or deleted by the provider. Remove only that exact old entry;
				// otherwise it would coexist with its newly qualified copy.
				removedLegacy := entry.ClusterMoID == "" &&
					slices.Contains(before.Status.ClusterModules, entry) &&
					!slices.Contains(after.Status.ClusterModules, entry)
				return owns(entry.ClusterMoID) || removedLegacy
			})
		for _, entry := range after.Status.ClusterModules {
			if owns(entry.ClusterMoID) {
				latest.Status.ClusterModules = append(latest.Status.ClusterModules, entry)
			}
		}
		if apiequality.Semantic.DeepEqual(base.Status, latest.Status) {
			return nil
		}
		return c.Status().Patch(ctx, latest, ctrlclient.MergeFromWithOptions(
			base, ctrlclient.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return fmt.Errorf("failed to patch resource policy shard status: %w", err)
	}
	return nil
}
