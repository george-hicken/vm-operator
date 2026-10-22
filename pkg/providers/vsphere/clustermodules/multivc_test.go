// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package clustermodules_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vimtypes "github.com/vmware/govmomi/vim25/types"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/clustermodules"
)

var _ = Describe("Cluster module status in multi-vCenter shard mode", func() {
	// Cluster MoIDs repeat across vCenters, so two shards each have a
	// "domain-c1".
	clusterRef := vimtypes.ManagedObjectReference{Type: "ClusterComputeResource", Value: "domain-c1"}

	shardCtx := func(sddc string) context.Context {
		return pkgcfg.UpdateContext(pkgcfg.NewContext(), func(config *pkgcfg.Config) {
			config.MultiVC = pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: sddc}
		})
	}

	It("Should key entries by the bare MoID outside shard mode", func() {
		ctx := pkgcfg.NewContext()
		Expect(clustermodules.StatusClusterKey(ctx, clusterRef)).To(Equal("domain-c1"))
		Expect(clustermodules.IsOwnStatusEntry(ctx, "vc-2/domain-c1")).To(BeTrue())
	})

	It("Should key each shard's entries by its SDDC, so equal MoIDs do not collide", func() {
		vc1, vc2 := shardCtx("vc-1"), shardCtx("vc-2")
		Expect(clustermodules.StatusClusterKey(vc1, clusterRef)).To(Equal("vc-1/domain-c1"))
		Expect(clustermodules.StatusClusterKey(vc2, clusterRef)).To(Equal("vc-2/domain-c1"))
		Expect(clustermodules.IsOwnStatusEntry(vc1, "vc-1/domain-c1")).To(BeTrue())
		Expect(clustermodules.IsOwnStatusEntry(vc1, "vc-2/domain-c1")).To(BeFalse())
		Expect(clustermodules.IsOwnStatusEntry(vc1, "domain-c1")).To(BeFalse())

		policy := &vmopv1.VirtualMachineSetResourcePolicy{
			Status: vmopv1.VirtualMachineSetResourcePolicyStatus{
				ClusterModules: []vmopv1.VSphereClusterModuleStatus{
					{GroupName: "cp", ModuleUuid: "uuid-vc1", ClusterMoID: "vc-1/domain-c1"},
					{GroupName: "cp", ModuleUuid: "uuid-vc2", ClusterMoID: "vc-2/domain-c1"},
				},
			},
		}
		_, uuid := clustermodules.FindClusterModuleUUID(vc2, "cp", clusterRef, policy)
		Expect(uuid).To(Equal("uuid-vc2"))
		_, uuid = clustermodules.FindClusterModuleUUID(vc1, "cp", clusterRef, policy)
		Expect(uuid).To(Equal("uuid-vc1"))
	})
})
