// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
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
		Expect(t.SDDCs).To(Equal([]string{"vc-1", "vc-2", "vc-3"}))
		Expect(t.IsSole()).To(BeFalse())
		Expect(t.Owned).To(Equal([]string{"zone-2"}))
		Expect(t.Foreign).To(Equal([]string{"zone-1", "zone-3"}))
		Expect(t.Unowned).To(Equal([]string{"zone-new"}))
	})
	It("Should fail for an SDDC that does not exist", func() {
		_, err := multivc.ComputeTopology(sddcs, azs, "vc-9")
		Expect(err).To(MatchError(multivc.ErrSDDCNotFound))
	})
	It("Should give every VM in an owned zone to exactly one shard, and no shard the rest", func() {
		shards := []string{"vc-1", "vc-2", "vc-3"}
		selectors := map[string]labels.Selector{}
		for _, s := range shards {
			selectors[s] = selectorFor(s)
		}
		for zone, want := range map[string][]string{
			"zone-1":   {"vc-1"},
			"zone-2":   {"vc-2"},
			"zone-3":   {"vc-3"},
			"zone-new": nil,
			"":         nil,
		} {
			var got []string
			for _, s := range shards {
				if selectors[s].Matches(vmLabels(zone)) {
					got = append(got, s)
				}
			}
			Expect(got).To(Equal(want), "zone %q", zone)
		}
	})
	It("Should select no VM for a shard that owns no zone", func() {
		azs = azs[:1]
		sel := selectorFor("vc-2")
		Expect(sel.Matches(vmLabels("zone-1"))).To(BeFalse())
		Expect(sel.Matches(vmLabels(""))).To(BeFalse())
	})
	It("Should select every VM, with or without a zone, for the sole shard", func() {
		sddcs = sddcs[:1]
		t, err := multivc.ComputeTopology(sddcs, azs, "vc-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(t.IsSole()).To(BeTrue())
		m := pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-1"}
		t.Apply(&m)
		Expect(m.Sole).To(BeTrue())
		sel, err := multivc.VMSelector(m)
		Expect(err).ToNot(HaveOccurred())
		Expect(sel.Empty()).To(BeTrue())
	})
	It("Should change the fingerprint when a second SDDC joins", func() {
		before, err := multivc.ComputeTopology(sddcs[:1], azs, "vc-1")
		Expect(err).ToNot(HaveOccurred())
		after, err := multivc.ComputeTopology(sddcs[:2], azs, "vc-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(after.Fingerprint()).ToNot(Equal(before.Fingerprint()))
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

var _ = Describe("Part", Label(testlabels.API), func() {
	It("Should run every part in combined mode", func() {
		for _, p := range []multivc.Part{multivc.PartGlobal, multivc.PartShard, multivc.PartSoleShard} {
			Expect(multivc.Runs(pkgcfg.MultiVC{}, p)).To(BeTrue())
		}
	})
	It("Should run only the global part in global mode", func() {
		m := pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal}
		Expect(multivc.Runs(m, multivc.PartGlobal)).To(BeTrue())
		Expect(multivc.Runs(m, multivc.PartShard)).To(BeFalse())
		Expect(multivc.Runs(m, multivc.PartSoleShard)).To(BeFalse())
	})
	It("Should run the sole-shard part only in a sole shard", func() {
		m := pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "s"}
		Expect(multivc.Runs(m, multivc.PartGlobal)).To(BeFalse())
		Expect(multivc.Runs(m, multivc.PartShard)).To(BeTrue())
		Expect(multivc.Runs(m, multivc.PartSoleShard)).To(BeFalse())
		m.Sole = true
		Expect(multivc.Runs(m, multivc.PartSoleShard)).To(BeTrue())
	})
})

var _ = Describe("Entitlement", Label(testlabels.API), func() {
	It("Should accept every SDDC with the allow-all verifier", func() {
		v := multivc.NewAllowAllVerifier(logr.Discard())
		obj := multivc.NewSDDC()
		obj.SetName("vc-2")
		Expect(v.Verify(context.Background(), obj)).To(Succeed())
		Expect(v.Verify(context.Background(), obj)).To(Succeed())
	})
})

var _ = Describe("Dependents", Label(testlabels.API), func() {
	var (
		client ctrlclient.Client
		vm     *vmopv1.VirtualMachine
	)

	BeforeEach(func() {
		vm = &vmopv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "vm-1"}}
		client = builder.NewFakeClient(vm)
	})

	admits := func(m pkgcfg.MultiVC, cached ctrlclient.Reader, name string) bool {
		snap := &vmopv1.VirtualMachineSnapshot{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "snap"}}
		snap.Spec.VMName = name
		p := multivc.DependentPredicate(m, cached, client, func(o ctrlclient.Object) string {
			return o.(*vmopv1.VirtualMachineSnapshot).Spec.VMName
		})
		return p.Generic(event.GenericEvent{Object: snap})
	}

	shard := pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-2", OwnedZones: "zone-2"}
	empty := builder.NewFakeClient()

	It("Should admit a dependent whose VM is in the shard's cache", func() {
		Expect(admits(shard, client, "vm-1")).To(BeTrue())
	})
	It("Should not admit a dependent whose VM is another shard's", func() {
		Expect(admits(shard, empty, "vm-1")).To(BeFalse())
	})
	It("Should admit a dependent whose VM does not exist, or that names none", func() {
		Expect(admits(shard, empty, "vm-gone")).To(BeTrue())
		Expect(admits(shard, empty, "")).To(BeTrue())
	})
	It("Should admit every dependent in the sole shard and outside shard mode", func() {
		sole := shard
		sole.Sole = true
		Expect(admits(sole, empty, "vm-1")).To(BeTrue())
		Expect(admits(pkgcfg.MultiVC{}, empty, "vm-1")).To(BeTrue())
	})
})

type readinessManager struct {
	ctrlmgr.Manager
	handler           healthz.Handler
	server            webhook.Server
	registrationError error
}

func (m *readinessManager) AddReadyzCheck(name string, check healthz.Checker) error {
	if m.registrationError != nil {
		return m.registrationError
	}
	m.handler.Checks = map[string]healthz.Checker{name: check}
	return nil
}

func (m *readinessManager) GetWebhookServer() webhook.Server {
	// Shard tests leave this nil: requesting a server would fail the test.
	return m.server
}

var _ = Describe("Readiness", Label(testlabels.API), func() {
	It("Should serve the shard readiness probe without a webhook server", func() {
		mgr := &readinessManager{}
		Expect(multivc.AddReadinessCheck(mgr, pkgcfg.MultiVC{
			Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-2",
		})).To(Succeed())
		mux := http.NewServeMux()
		mux.Handle("/readyz", http.StripPrefix("/readyz", &mgr.handler))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		Expect(response.Code).To(Equal(http.StatusOK))
	})

	DescribeTable("Should retain webhook readiness for processes serving webhooks", func(mode pkgcfg.MultiVCMode) {
		mgr := &readinessManager{server: webhook.NewServer(webhook.Options{})}
		Expect(multivc.AddReadinessCheck(mgr, pkgcfg.MultiVC{Mode: mode})).To(Succeed())
		Expect(mgr.handler.Checks).To(HaveKey("webhook"))
		response := httptest.NewRecorder()
		mgr.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		// An unstarted webhook server must keep these processes unready.
		Expect(response.Code).To(Equal(http.StatusInternalServerError))
	},
		Entry("combined", pkgcfg.MultiVCModeCombined),
		Entry("global", pkgcfg.MultiVCModeGlobal),
	)

	It("Should report readiness registration failures", func() {
		mgr := &readinessManager{registrationError: errors.New("registration failed")}
		Expect(multivc.AddReadinessCheck(mgr, pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard})).To(
			MatchError(ContainSubstring("registration failed")))
	})
})

var _ = Describe("Resource policy shard status", Label(testlabels.API), func() {
	var (
		ctx    context.Context
		policy *vmopv1.VirtualMachineSetResourcePolicy
		scheme *runtime.Scheme
	)
	shardContext := func(sddc string) context.Context {
		return pkgcfg.UpdateContext(pkgcfg.NewContext(), func(config *pkgcfg.Config) {
			config.MultiVC = pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: sddc}
		})
	}
	setEntries := func(policy *vmopv1.VirtualMachineSetResourcePolicy, sddc, uuid string) {
		policy.Status.ResourcePools = append(policy.Status.ResourcePools, vmopv1.ResourcePoolStatus{
			ClusterMoID: sddc + "/domain-c1", ChildResourcePoolMoID: "resgroup-" + sddc,
		})
		policy.Status.ClusterModules = append(policy.Status.ClusterModules, vmopv1.VSphereClusterModuleStatus{
			ClusterMoID: sddc + "/domain-c1", GroupName: "cp", ModuleUuid: uuid,
		})
	}
	BeforeEach(func() {
		ctx = shardContext("vc-2")
		scheme = runtime.NewScheme()
		Expect(vmopv1.AddToScheme(scheme)).To(Succeed())
		policy = &vmopv1.VirtualMachineSetResourcePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "ns", UID: types.UID("policy-uid")},
		}
	})

	It("Should preserve all writers when stale shards create entries and a status patch conflicts", func() {
		patches := 0
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).
			WithStatusSubresource(policy).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c ctrlclient.Client, subresource string,
					obj ctrlclient.Object, patch ctrlclient.Patch, opts ...ctrlclient.SubResourcePatchOption) error {
					patches++
					if patches == 2 {
						latest := &vmopv1.VirtualMachineSetResourcePolicy{}
						Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(obj), latest)).To(Succeed())
						setEntries(latest, "vc-3", "module-vc3")
						Expect(c.Status().Update(ctx, latest)).To(Succeed())
					}
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
			}).Build()
		before := policy.DeepCopy()
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), before)).To(Succeed())
		first, second := before.DeepCopy(), before.DeepCopy()
		setEntries(first, "vc-1", "module-vc1")
		setEntries(second, "vc-2", "already-created-module-vc2")
		Expect(multivc.PatchResourcePolicyStatus(shardContext("vc-1"), c, c, before, first)).To(Succeed())
		Expect(multivc.PatchResourcePolicyStatus(ctx, c, c, before, second)).To(Succeed())
		latest := &vmopv1.VirtualMachineSetResourcePolicy{}
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), latest)).To(Succeed())
		Expect(patches).To(Equal(3))
		Expect(latest.Status.ResourcePools).To(HaveLen(3))
		Expect(latest.Status.ClusterModules).To(ConsistOf(
			first.Status.ClusterModules[0], second.Status.ClusterModules[0],
			vmopv1.VSphereClusterModuleStatus{ClusterMoID: "vc-3/domain-c1", GroupName: "cp", ModuleUuid: "module-vc3"},
		))
		Expect(second.Status.ClusterModules[0].ModuleUuid).To(Equal("already-created-module-vc2"))
	})

	It("Should remove only the deleting shard's entries and preserve newer foreign entries", func() {
		setEntries(policy, "vc-1", "old-module-vc1")
		setEntries(policy, "vc-2", "module-vc2")
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).WithStatusSubresource(policy).Build()
		before := policy.DeepCopy()
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), before)).To(Succeed())
		after := before.DeepCopy()
		after.Status.ResourcePools = after.Status.ResourcePools[:1]
		after.Status.ClusterModules = after.Status.ClusterModules[:1]
		fresh := before.DeepCopy()
		fresh.Status.ClusterModules[0].ModuleUuid = "new-module-vc1"
		Expect(c.Status().Update(ctx, fresh)).To(Succeed())
		Expect(multivc.PatchResourcePolicyStatus(ctx, c, c, before, after)).To(Succeed())
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), fresh)).To(Succeed())
		Expect(fresh.Status.ResourcePools).To(ConsistOf(before.Status.ResourcePools[0]))
		Expect(fresh.Status.ClusterModules).To(ConsistOf(vmopv1.VSphereClusterModuleStatus{
			ClusterMoID: "vc-1/domain-c1", GroupName: "cp", ModuleUuid: "new-module-vc1",
		}))
	})

	It("Should replace a claimed legacy entry while retaining other legacy entries", func() {
		claimed := vmopv1.VSphereClusterModuleStatus{GroupName: "cp", ModuleUuid: "claimed"}
		foreign := vmopv1.VSphereClusterModuleStatus{GroupName: "cp", ModuleUuid: "foreign"}
		policy.Status.ClusterModules = []vmopv1.VSphereClusterModuleStatus{claimed, foreign}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).WithStatusSubresource(policy).Build()
		before := policy.DeepCopy()
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), before)).To(Succeed())
		after := before.DeepCopy()
		after.Status.ClusterModules[0].ClusterMoID = "vc-2/domain-c1"
		Expect(multivc.PatchResourcePolicyStatus(ctx, c, c, before, after)).To(Succeed())
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), policy)).To(Succeed())
		Expect(policy.Status.ClusterModules).To(ConsistOf(after.Status.ClusterModules))
	})

	It("Should skip an unchanged status", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, ctrlclient.WithWatch, ctrlclient.ObjectKey, ctrlclient.Object, ...ctrlclient.GetOption) error {
					return fmt.Errorf("unexpected read")
				},
			}).Build()
		Expect(multivc.PatchResourcePolicyStatus(ctx, c, c, policy, policy.DeepCopy())).To(Succeed())
	})

	It("Should report exhausted conflicts without changing the intended module UUID", func() {
		patches := 0
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).WithStatusSubresource(policy).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(context.Context, ctrlclient.Client, string, ctrlclient.Object,
					ctrlclient.Patch, ...ctrlclient.SubResourcePatchOption) error {
					patches++
					return apierrors.NewConflict(schema.GroupResource{Resource: "virtualmachinesetresourcepolicies"}, policy.Name, fmt.Errorf("concurrent write"))
				},
			}).Build()
		before := policy.DeepCopy()
		Expect(c.Get(ctx, ctrlclient.ObjectKeyFromObject(policy), before)).To(Succeed())
		after := before.DeepCopy()
		setEntries(after, "vc-2", "created-once")
		err := multivc.PatchResourcePolicyStatus(ctx, c, c, before, after)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(patches).To(BeNumerically(">", 1))
		Expect(after.Status.ClusterModules[0].ModuleUuid).To(Equal("created-once"))
	})

	It("Should reject a policy that was replaced while reconciling", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).WithStatusSubresource(policy).Build()
		before := policy.DeepCopy()
		before.UID = "old-policy-uid"
		after := before.DeepCopy()
		setEntries(after, "vc-2", "created-once")
		Expect(multivc.PatchResourcePolicyStatus(ctx, c, c, before, after)).To(MatchError(ContainSubstring("was replaced")))
	})
})
