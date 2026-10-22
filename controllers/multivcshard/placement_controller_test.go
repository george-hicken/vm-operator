// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivcshard_test

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	"github.com/vmware-tanzu/vm-operator/controllers/multivcshard"
	topologyv1 "github.com/vmware-tanzu/vm-operator/external/tanzu-topology/api/v1alpha1"
	pkgcond "github.com/vmware-tanzu/vm-operator/pkg/conditions"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

var _ = Describe("Placement", Label(testlabels.Controller), func() {
	const namespace = "ns-1"

	var (
		ctx      context.Context
		client   ctrlclient.Client
		r        *multivcshard.PlacementReconciler
		objs     []ctrlclient.Object
		verifier multivc.EntitlementVerifier
		chosen   []int
	)

	zone := func(name, sddc string) []ctrlclient.Object {
		az := &topologyv1.AvailabilityZone{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if sddc != "" {
			az.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: "iaas.vmware.com/v1alpha1", Kind: "SDDC", Name: sddc, UID: "uid",
			}}
		}
		return []ctrlclient.Object{az, &topologyv1.Zone{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}}
	}

	vm := func(zone string) *vmopv1.VirtualMachine {
		o := &vmopv1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Name: "vm", Namespace: namespace}}
		if zone != "" {
			o.Labels = map[string]string{corev1.LabelTopologyZone: zone}
		}
		return o
	}

	BeforeEach(func() {
		verifier = multivc.NewAllowAllVerifier(logr.Discard())
		chosen = nil
		objs = []ctrlclient.Object{newSDDC("vc-1"), newSDDC("vc-2"), vm("")}
		objs = append(objs, zone("zone-1", "vc-1")...)
		objs = append(objs, zone("zone-2", "vc-2")...)
		objs = append(objs, zone("zone-unowned", "")...)
	})

	JustBeforeEach(func() {
		config := testConfig()
		config.Features.WorkloadDomainIsolation = true
		ctx = pkgcfg.WithConfig(config)
		client = builder.NewFakeClient(objs...)
		r = &multivcshard.PlacementReconciler{
			Client:   client,
			Context:  ctx,
			Logger:   logr.Discard(),
			Verifier: verifier,
			Choose: func(n int) int {
				chosen = append(chosen, n)
				return n - 1
			},
		}
	})

	reconcile := func() *vmopv1.VirtualMachine {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKey{Namespace: namespace, Name: "vm"}})
		Expect(err).ToNot(HaveOccurred())
		out := &vmopv1.VirtualMachine{}
		Expect(client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: "vm"}, out)).To(Succeed())
		return out
	}

	It("Should label a VM with no zone with one of the entitled SDDCs' zones", func() {
		out := reconcile()
		Expect(chosen).To(Equal([]int{2}))
		Expect(out.Labels[corev1.LabelTopologyZone]).To(Equal("zone-2"))
		Expect(pkgcond.Get(out, multivc.ConditionPlaced)).To(BeNil())

		By("leaving a VM that has a zone as it is", func() {
			Expect(reconcile().Labels[corev1.LabelTopologyZone]).To(Equal("zone-2"))
			Expect(chosen).To(HaveLen(1))
		})
	})

	It("Should preserve a concurrent zone assignment and return a conflict for retry", func() {
		key := ctrlclient.ObjectKey{Namespace: namespace, Name: "vm"}
		patches := 0
		r.Client = interceptor.NewClient(client.(ctrlclient.WithWatch), interceptor.Funcs{
			Patch: func(ctx context.Context, c ctrlclient.WithWatch, obj ctrlclient.Object, patch ctrlclient.Patch, opts ...ctrlclient.PatchOption) error {
				patches++
				By("persisting another writer's zone after the placement read")
				winner := &vmopv1.VirtualMachine{}
				Expect(client.Get(ctx, key, winner)).To(Succeed())
				winner.Labels = map[string]string{corev1.LabelTopologyZone: "zone-1"}
				Expect(client.Update(ctx, winner)).To(Succeed())
				return c.Patch(ctx, obj, patch, opts...)
			},
		})

		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		winner := &vmopv1.VirtualMachine{}
		Expect(client.Get(ctx, key, winner)).To(Succeed())
		Expect(winner.Labels[corev1.LabelTopologyZone]).To(Equal("zone-1"))
		Expect(chosen).To(Equal([]int{2}))

		By("retrying against the persisted winner without another assignment")
		Expect(reconcile().Labels[corev1.LabelTopologyZone]).To(Equal("zone-1"))
		Expect(patches).To(Equal(1))
		Expect(chosen).To(HaveLen(1))
	})

	When("the namespace has no zone", func() {
		BeforeEach(func() {
			for i := len(objs) - 1; i >= 0; i-- {
				if _, ok := objs[i].(*topologyv1.Zone); ok {
					objs = append(objs[:i], objs[i+1:]...)
				}
			}
		})

		It("Should keep retrying an unchanged condition and place after dependency recovery", func() {
			key := ctrlclient.ObjectKey{Namespace: namespace, Name: "vm"}
			result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))
			waiting := &vmopv1.VirtualMachine{}
			Expect(client.Get(ctx, key, waiting)).To(Succeed())
			Expect(pkgcond.Get(waiting, multivc.ConditionPlaced).Reason).To(Equal(multivc.ReasonNoEligibleZone))
			rv := waiting.ResourceVersion

			By("retaining the retry when NoEligibleZone no longer needs a write")
			result, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))
			Expect(client.Get(ctx, key, waiting)).To(Succeed())
			Expect(waiting.ResourceVersion).To(Equal(rv))

			By("recovering eligibility without updating the VM")
			Expect(client.Create(ctx, &topologyv1.Zone{ObjectMeta: metav1.ObjectMeta{Name: "zone-1", Namespace: namespace}})).To(Succeed())
			Expect(client.Get(ctx, key, waiting)).To(Succeed())
			Expect(waiting.ResourceVersion).To(Equal(rv))
			Expect(reconcile().Labels[corev1.LabelTopologyZone]).To(Equal("zone-1"))
			Expect(chosen).To(Equal([]int{1}))
			Expect(pkgcond.Get(reconcile(), multivc.ConditionPlaced)).To(BeNil())
		})
	})

	When("the Supervisor has one SDDC", func() {
		BeforeEach(func() {
			objs = objs[1:]
		})
		It("Should leave the VM for the sole shard to place", func() {
			out := reconcile()
			Expect(out.Labels).ToNot(HaveKey(corev1.LabelTopologyZone))
			Expect(chosen).To(BeEmpty())
		})
	})

	When("no SDDC is entitled", func() {
		BeforeEach(func() {
			verifier = denyVerifier{}
		})
		It("Should report NoEligibleZone, and clear it once the VM has a zone", func() {
			out := reconcile()
			Expect(out.Labels).ToNot(HaveKey(corev1.LabelTopologyZone))
			c := pkgcond.Get(out, multivc.ConditionPlaced)
			Expect(c).ToNot(BeNil())
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(multivc.ReasonNoEligibleZone))

			out.Labels = map[string]string{corev1.LabelTopologyZone: "zone-1"}
			Expect(client.Update(ctx, out)).To(Succeed())
			Expect(pkgcond.Get(reconcile(), multivc.ConditionPlaced)).To(BeNil())
		})
	})
})
