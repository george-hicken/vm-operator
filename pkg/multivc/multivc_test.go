// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc_test

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	topologyv1 "github.com/vmware-tanzu/vm-operator/external/tanzu-topology/api/v1alpha1"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

func sddcObj(name string, created time.Time, endpoint string) *unstructured.Unstructured {
	obj := multivc.NewSDDC()
	obj.SetName(name)
	obj.SetCreationTimestamp(metav1.NewTime(created))
	obj.SetAnnotations(map[string]string{
		multivc.AnnotationVCenterUUID: "uuid-" + name,
		multivc.AnnotationDatacenter:  "datacenter-2",
	})
	Expect(unstructured.SetNestedField(obj.Object, endpoint, "spec", "endpointURL")).To(Succeed())
	return obj
}

func azOwnedBy(name, sddc string) topologyv1.AvailabilityZone {
	az := topologyv1.AvailabilityZone{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if sddc != "" {
		az.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "iaas.vmware.com/v1alpha1",
			Kind:       "SDDC",
			Name:       sddc,
			UID:        "uid-" + "sddc",
		}}
	}
	return az
}

func vmLabels(zone string) labels.Set {
	if zone == "" {
		return labels.Set{}
	}
	return labels.Set{corev1.LabelTopologyZone: zone}
}

var _ = Describe("Identity", Label(testlabels.API), func() {
	It("Should accept the combined, global and shard modes", func() {
		Expect(multivc.ValidateIdentity(pkgcfg.MultiVC{})).To(Succeed())
		Expect(multivc.ValidateIdentity(pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal})).To(Succeed())
		Expect(multivc.ValidateIdentity(pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "s"})).To(Succeed())
	})
	It("Should reject an unknown mode, a shard without an SDDC, and an SDDC outside shard mode", func() {
		Expect(multivc.ValidateIdentity(pkgcfg.MultiVC{Mode: "both"})).To(MatchError(multivc.ErrInvalidMode))
		Expect(multivc.ValidateIdentity(pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard})).To(MatchError(multivc.ErrInvalidMode))
		Expect(multivc.ValidateIdentity(pkgcfg.MultiVC{ShardSDDC: "s"})).To(MatchError(multivc.ErrInvalidMode))
	})
	It("Should suffix the leader election ID outside combined mode", func() {
		Expect(multivc.LeaderElectionID("vmop", pkgcfg.MultiVC{})).To(Equal("vmop"))
		Expect(multivc.LeaderElectionID("vmop", pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal})).To(Equal("vmop-global"))
		Expect(multivc.LeaderElectionID("vmop",
			pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-2"})).To(Equal("vmop-vc-2"))
	})
})

var _ = Describe("SDDC", Label(testlabels.API), func() {
	now := time.Now().Truncate(time.Second)

	It("Should read the endpoint, trust and identity of an SDDC", func() {
		s := multivc.SDDCFromUnstructured(sddcObj("vc-1", now, "https://vc1.example.com:8443/sdk"))
		Expect(s.VCenterUUID).To(Equal("uuid-vc-1"))
		Expect(s.Datacenter).To(Equal("datacenter-2"))
		host, port, err := s.Endpoint()
		Expect(err).ToNot(HaveOccurred())
		Expect(host).To(Equal("vc1.example.com"))
		Expect(port).To(Equal("8443"))
	})
	It("Should default the port to 443 and reject a non-https endpoint", func() {
		_, port, err := multivc.SDDC{EndpointURL: "https://10.1.2.10"}.Endpoint()
		Expect(err).ToNot(HaveOccurred())
		Expect(port).To(Equal("443"))
		_, _, err = multivc.SDDC{EndpointURL: "http://10.1.2.10"}.Endpoint()
		Expect(err).To(HaveOccurred())
	})
	It("Should pick the oldest SDDC as primary, breaking ties by name", func() {
		sddcs := []multivc.SDDC{
			{Name: "b", CreationTimestamp: metav1.NewTime(now)},
			{Name: "c", CreationTimestamp: metav1.NewTime(now.Add(time.Minute))},
			{Name: "a", CreationTimestamp: metav1.NewTime(now)},
		}
		Expect(multivc.PrimarySDDC(sddcs)).To(Equal("a"))
		Expect(multivc.PrimarySDDC(nil)).To(BeEmpty())
	})
})

var _ = Describe("Topology and selector", Label(testlabels.API), func() {
	var (
		sddcs []multivc.SDDC
		azs   []topologyv1.AvailabilityZone
	)

	BeforeEach(func() {
		now := time.Now().Truncate(time.Second)
		sddcs = []multivc.SDDC{
			{Name: "vc-1", CreationTimestamp: metav1.NewTime(now), EndpointURL: "https://vc1"},
			{Name: "vc-2", CreationTimestamp: metav1.NewTime(now.Add(time.Hour)), EndpointURL: "https://vc2"},
			{Name: "vc-3", CreationTimestamp: metav1.NewTime(now.Add(2 * time.Hour)), EndpointURL: "https://vc3"},
		}
		azs = []topologyv1.AvailabilityZone{
			azOwnedBy("zone-1", "vc-1"),
			azOwnedBy("zone-2", "vc-2"),
			azOwnedBy("zone-3", "vc-3"),
			azOwnedBy("zone-new", ""),
		}
	})

	selectorFor := func(sddc string) labels.Selector {
		t, err := multivc.ComputeTopology(sddcs, azs, sddc)
		Expect(err).ToNot(HaveOccurred())
		m := pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: sddc}
		t.Apply(&m)
		sel, err := multivc.VMSelector(m)
		Expect(err).ToNot(HaveOccurred())
		return sel
	}

	It("Should resolve each AvailabilityZone to the SDDC that owns it", func() {
		t, err := multivc.ComputeTopology(sddcs, azs, "vc-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(t.Primary).To(Equal("vc-1"))
		Expect(t.IsPrimary()).To(BeFalse())
		Expect(t.Owned).To(Equal([]string{"zone-2"}))
		Expect(t.Foreign).To(Equal([]string{"zone-1", "zone-3"}))
		Expect(t.Unowned).To(Equal([]string{"zone-new"}))
	})
	It("Should fail for an SDDC that does not exist", func() {
		_, err := multivc.ComputeTopology(sddcs, azs, "vc-9")
		Expect(err).To(MatchError(multivc.ErrSDDCNotFound))
	})
	It("Should give every VM to exactly one shard", func() {
		shards := []string{"vc-1", "vc-2", "vc-3"}
		selectors := map[string]labels.Selector{}
		for _, s := range shards {
			selectors[s] = selectorFor(s)
		}
		for zone, want := range map[string]string{
			"zone-1":   "vc-1",
			"zone-2":   "vc-2",
			"zone-3":   "vc-3",
			"zone-new": "vc-1",
			"":         "vc-1",
		} {
			var got []string
			for _, s := range shards {
				if selectors[s].Matches(vmLabels(zone)) {
					got = append(got, s)
				}
			}
			Expect(got).To(Equal([]string{want}), "zone %q", zone)
		}
	})
	It("Should select no VM for a non-primary shard that owns no zone", func() {
		azs = azs[:1]
		sel := selectorFor("vc-2")
		Expect(sel.Matches(vmLabels("zone-1"))).To(BeFalse())
		Expect(sel.Matches(vmLabels(""))).To(BeFalse())
	})
	It("Should select every VM for a primary shard when no zone is foreign", func() {
		azs = azs[:1]
		sel := selectorFor("vc-1")
		Expect(sel.Empty()).To(BeTrue())
	})
	It("Should select every VM outside shard mode", func() {
		sel, err := multivc.VMSelector(pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal})
		Expect(err).ToNot(HaveOccurred())
		Expect(sel.Empty()).To(BeTrue())
	})
	It("Should change the fingerprint when a zone moves to another SDDC", func() {
		before, err := multivc.ComputeTopology(sddcs, azs, "vc-2")
		Expect(err).ToNot(HaveOccurred())
		azs[3] = azOwnedBy("zone-new", "vc-3")
		after, err := multivc.ComputeTopology(sddcs, azs, "vc-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(after.Fingerprint()).ToNot(Equal(before.Fingerprint()))
	})
})

var _ = Describe("Topology watcher", Label(testlabels.API), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		client ctrlclient.Client
	)

	BeforeEach(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		now := time.Now().Truncate(time.Second)
		az := azOwnedBy("zone-2", "vc-2")
		client = builder.NewFakeClient(
			sddcObj("vc-1", now, "https://vc1"),
			sddcObj("vc-2", now.Add(time.Hour), "https://vc2"),
			&az)
	})

	AfterEach(func() {
		cancel()
	})

	It("Should return ErrTopologyChanged once a zone moves", func() {
		t, err := multivc.LoadTopology(ctx, client, "vc-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(t.Owned).To(Equal([]string{"zone-2"}))

		done := make(chan error, 1)
		go func() {
			done <- multivc.NewTopologyWatcher(client, "vc-2", t.Fingerprint(),
				10*time.Millisecond, logr.Discard())(ctx)
		}()
		Consistently(done, 100*time.Millisecond).ShouldNot(Receive())

		az := &topologyv1.AvailabilityZone{}
		Expect(client.Get(ctx, ctrlclient.ObjectKey{Name: "zone-2"}, az)).To(Succeed())
		az.OwnerReferences[0].Name = "vc-1"
		Expect(client.Update(ctx, az)).To(Succeed())

		Eventually(done).Should(Receive(MatchError(multivc.ErrTopologyChanged)))
	})

	It("Should return nil when its context ends", func() {
		cctx, ccancel := context.WithCancel(ctx)
		ccancel()
		Expect(multivc.NewTopologyWatcher(client, "vc-2", "", time.Hour, logr.Discard())(cctx)).To(Succeed())
	})
})
