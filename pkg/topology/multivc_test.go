// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package topology_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	topologyv1 "github.com/vmware-tanzu/vm-operator/external/tanzu-topology/api/v1alpha1"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/topology"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

var _ = Describe("Zones in multi-vCenter shard mode", func() {
	const namespace = "ns-1"

	var (
		ctx     context.Context
		client  ctrlclient.Client
		multiVC pkgcfg.MultiVC
	)

	BeforeEach(func() {
		var objs []ctrlclient.Object
		for _, name := range []string{"zone-1", "zone-2", "zone-new"} {
			objs = append(objs,
				&topologyv1.AvailabilityZone{
					ObjectMeta: metav1.ObjectMeta{Name: name},
				},
				&topologyv1.Zone{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: topologyv1.ZoneSpec{
						ManagedVMs: topologyv1.VSphereEntityInfo{
							FolderMoID: "folder-" + name,
							PoolMoIDs:  []string{"pool-" + name},
						},
					},
				})
		}
		client = builder.NewFakeClient(objs...)
		multiVC = pkgcfg.MultiVC{}
	})

	JustBeforeEach(func() {
		ctx = pkgcfg.UpdateContext(
			pkgcfg.NewContextWithDefaultConfig(),
			func(config *pkgcfg.Config) {
				config.MultiVC = multiVC
				config.Features.WorkloadDomainIsolation = true
			})
	})

	names := func(objs []topologyv1.Zone) []string {
		var out []string
		for _, o := range objs {
			out = append(out, o.Name)
		}
		return out
	}

	When("the process is not a shard", func() {
		It("Should return every zone", func() {
			zones, err := topology.GetZones(ctx, client, namespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(names(zones)).To(ConsistOf("zone-1", "zone-2", "zone-new"))
		})
	})

	When("the process is a non-primary shard", func() {
		BeforeEach(func() {
			multiVC = pkgcfg.MultiVC{
				Mode:         pkgcfg.MultiVCModeShard,
				ShardSDDC:    "vc-2",
				OwnedZones:   "zone-2",
				ForeignZones: "zone-1",
			}
		})

		It("Should return only its zones and availability zones", func() {
			zones, err := topology.GetZones(ctx, client, namespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(names(zones)).To(ConsistOf("zone-2"))

			azs, err := topology.GetAvailabilityZones(ctx, client)
			Expect(err).ToNot(HaveOccurred())
			Expect(azs).To(HaveLen(1))
			Expect(azs[0].Name).To(Equal("zone-2"))
		})
		It("Should report another shard's zone as not found", func() {
			_, err := topology.GetZone(ctx, client, "zone-1", namespace)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			_, err = topology.GetAvailabilityZone(ctx, client, "zone-new")
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
		It("Should return the folder and pools of its zones only", func() {
			folder, pools, err := topology.GetNamespaceFolderAndRPMoIDs(ctx, client, namespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(folder).To(Equal("folder-zone-2"))
			Expect(pools).To(ConsistOf("pool-zone-2"))
		})
	})

	When("the process is the primary shard", func() {
		BeforeEach(func() {
			multiVC = pkgcfg.MultiVC{
				Mode:         pkgcfg.MultiVCModeShard,
				ShardSDDC:    "vc-1",
				Primary:      true,
				OwnedZones:   "zone-1",
				ForeignZones: "zone-2",
			}
		})

		It("Should return its zones and the unowned zones", func() {
			zones, err := topology.GetZones(ctx, client, namespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(names(zones)).To(ConsistOf("zone-1", "zone-new"))

			_, err = topology.GetZone(ctx, client, "zone-new", namespace)
			Expect(err).ToNot(HaveOccurred())
			_, err = topology.GetZone(ctx, client, "zone-2", namespace)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	When("a shard owns no zone", func() {
		BeforeEach(func() {
			multiVC = pkgcfg.MultiVC{
				Mode:         pkgcfg.MultiVCModeShard,
				ShardSDDC:    "vc-3",
				ForeignZones: "zone-1,zone-2",
			}
		})

		It("Should report no zones", func() {
			_, err := topology.GetZones(ctx, client, namespace)
			Expect(err).To(MatchError(topology.ErrNoZones))
			_, err = topology.GetAvailabilityZones(ctx, client)
			Expect(err).To(MatchError(topology.ErrNoAvailabilityZones))
		})
	})
})
