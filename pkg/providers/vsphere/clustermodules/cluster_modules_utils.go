// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package clustermodules

import (
	"context"
	"strings"

	vimtypes "github.com/vmware/govmomi/vim25/types"
	apierrorsutil "k8s.io/apimachinery/pkg/util/errors"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// StatusClusterKey returns the value that a status entry records as the
// cluster MoID of the cluster.
//
// In multi-vCenter shard mode it is "<sddc>/<moid>": every shard writes the
// status of the same resource policy, and cluster MoIDs repeat across
// vCenters, so a bare MoID cannot say whose cluster an entry is for.
func StatusClusterKey(ctx context.Context, clusterRef vimtypes.ManagedObjectReference) string {
	if m := pkgcfg.FromContext(ctx).MultiVC; m.IsShard() {
		return m.ShardSDDC + "/" + clusterRef.Value
	}
	return clusterRef.Value
}

// IsOwnStatusEntry returns true when a status entry with the given cluster
// MoID was written by this process. Outside multi-vCenter shard mode that is
// every entry. A shard leaves the other shards' entries as they are.
func IsOwnStatusEntry(ctx context.Context, clusterMoID string) bool {
	if m := pkgcfg.FromContext(ctx).MultiVC; m.IsShard() {
		return strings.HasPrefix(clusterMoID, m.ShardSDDC+"/")
	}
	return true
}

// FindClusterModuleUUID returns the index in the Status.ClusterModules and UUID of the
// VC cluster module for the given groupName and cluster reference.
func FindClusterModuleUUID(
	ctx context.Context,
	groupName string,
	clusterRef vimtypes.ManagedObjectReference,
	resourcePolicy *vmopv1.VirtualMachineSetResourcePolicy) (int, string) {

	key := StatusClusterKey(ctx, clusterRef)
	for i, modStatus := range resourcePolicy.Status.ClusterModules {
		if modStatus.GroupName == groupName && (modStatus.ClusterMoID == key) {
			return i, modStatus.ModuleUuid
		}
	}

	return -1, ""
}

// ClaimClusterModuleUUID tries to find an existing entry in the Status.ClusterModules that is for
// the given groupName and cluster reference. This is meant for after an upgrade where the FaultDomains
// FSS is now enabled but we had not previously set the ClusterMoID.
func ClaimClusterModuleUUID(
	ctx context.Context,
	clusterModProvider Provider,
	groupName string,
	clusterRef vimtypes.ManagedObjectReference,
	resourcePolicy *vmopv1.VirtualMachineSetResourcePolicy) (int, string, error) {

	var errs []error

	for i, modStatus := range resourcePolicy.Status.ClusterModules {
		if modStatus.GroupName == groupName && modStatus.ClusterMoID == "" {
			exists, err := clusterModProvider.DoesModuleExist(ctx, modStatus.ModuleUuid, clusterRef)
			if err != nil {
				errs = append(errs, err)
			} else if exists {
				return i, modStatus.ModuleUuid, nil
			}
		}
	}

	return -1, "", apierrorsutil.NewAggregate(errs)
}
