// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package virtualmachinesetresourcepolicy_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinesetresourcepolicy"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	providerfake "github.com/vmware-tanzu/vm-operator/pkg/providers/fake"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

type conflictingPolicyClient struct {
	ctrlclient.Client
	conflicted bool
}

func (c *conflictingPolicyClient) Patch(ctx context.Context, obj ctrlclient.Object, patch ctrlclient.Patch, opts ...ctrlclient.PatchOption) error {
	if !c.conflicted {
		c.conflicted = true
		latest := &vmopv1.VirtualMachineSetResourcePolicy{}
		if err := c.Client.Get(ctx, ctrlclient.ObjectKeyFromObject(obj), latest); err != nil {
			return err
		}
		controllerutil.AddFinalizer(latest, "example.com/other-owner")
		if err := c.Client.Update(ctx, latest); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{
			Group: vmopv1.GroupVersion.Group, Resource: "virtualmachinesetresourcepolicies",
		}, obj.GetName(), fmt.Errorf("concurrent owner update"))
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

var _ = Describe("Resource policy shard deletion fence", Label(testlabels.Controller, testlabels.API), func() {
	var (
		ctx      context.Context
		c        ctrlclient.Client
		key      ctrlclient.ObjectKey
		provider *providerfake.VMProvider
	)
	BeforeEach(func() {
		ctx = pkgcfg.NewContext()
		pkgcfg.SetContext(ctx, func(config *pkgcfg.Config) {
			config.MultiVC = pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-1"}
		})
		policy := &vmopv1.VirtualMachineSetResourcePolicy{ObjectMeta: metav1.ObjectMeta{
			Name: "sharded-policy", Namespace: "ns", UID: "policy-uid",
		}}
		key = ctrlclient.ObjectKeyFromObject(policy)
		c = builder.NewFakeClient(policy)
		provider = providerfake.NewVMProvider()
	})
	getPolicy := func() *vmopv1.VirtualMachineSetResourcePolicy {
		latest := &vmopv1.VirtualMachineSetResourcePolicy{}
		Expect(c.Get(ctx, key, latest)).To(Succeed())
		return latest
	}
	reconcile := func() error {
		r := virtualmachinesetresourcepolicy.NewReconciler(ctx, c, logr.Discard(), provider)
		r.APIReader = c
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		return err
	}

	It("persists the common gate before the provider can create resources", func() {
		provider.CreateOrUpdateVirtualMachineSetResourcePolicyFn = func(_ context.Context, _ *vmopv1.VirtualMachineSetResourcePolicy) error {
			Expect(getPolicy().Finalizers).To(Equal([]string{finalizer}))
			return nil
		}
		Expect(reconcile()).To(Succeed())
		Expect(getPolicy().Finalizers).To(Equal([]string{finalizer}))
	})

	It("rejects a direct provider reconcile without a persisted gate", func() {
		called := false
		provider.CreateOrUpdateVirtualMachineSetResourcePolicyFn = func(_ context.Context, _ *vmopv1.VirtualMachineSetResourcePolicy) error {
			called = true
			return nil
		}
		r := virtualmachinesetresourcepolicy.NewReconciler(ctx, c, logr.Discard(), provider)
		err := r.ReconcileNormal(&pkgctx.VirtualMachineSetResourcePolicyContext{
			Context: ctx, Logger: logr.Discard(), ResourcePolicy: getPolicy(),
		})
		Expect(err).To(MatchError(ContainSubstring("no persisted deletion gate")))
		Expect(called).To(BeFalse())
	})

	It("preserves another metadata writer when registration conflicts", func() {
		c = &conflictingPolicyClient{Client: c}
		Expect(reconcile()).To(Succeed())
		Expect(getPolicy().Finalizers).To(ConsistOf(finalizer, "example.com/other-owner"))
	})

	It("keeps the gate and skips provider deletion after creation starts deletion", func() {
		provider.CreateOrUpdateVirtualMachineSetResourcePolicyFn = func(_ context.Context, _ *vmopv1.VirtualMachineSetResourcePolicy) error {
			return c.Delete(ctx, getPolicy())
		}
		deleted := false
		provider.DeleteVirtualMachineSetResourcePolicyFn = func(_ context.Context, _ *vmopv1.VirtualMachineSetResourcePolicy) error {
			deleted = true
			return nil
		}
		Expect(reconcile()).To(Succeed())
		Expect(getPolicy().DeletionTimestamp.IsZero()).To(BeFalse())
		err := reconcile()
		Expect(errors.Is(err, virtualmachinesetresourcepolicy.ErrShardPolicyDeletionUnsupported)).To(BeTrue())
		Expect(deleted).To(BeFalse())
		Expect(getPolicy().Finalizers).To(Equal([]string{finalizer}))
	})

	It("retains a preexisting deleting policy's shared gate", func() {
		latest := getPolicy()
		latest.Finalizers = []string{finalizer}
		Expect(c.Update(ctx, latest)).To(Succeed())
		Expect(c.Delete(ctx, latest)).To(Succeed())
		created, deleted := false, false
		provider.CreateOrUpdateVirtualMachineSetResourcePolicyFn = func(_ context.Context, _ *vmopv1.VirtualMachineSetResourcePolicy) error {
			created = true
			return nil
		}
		provider.DeleteVirtualMachineSetResourcePolicyFn = func(_ context.Context, _ *vmopv1.VirtualMachineSetResourcePolicy) error {
			deleted = true
			return nil
		}
		err := reconcile()
		Expect(errors.Is(err, virtualmachinesetresourcepolicy.ErrShardPolicyDeletionUnsupported)).To(BeTrue())
		Expect(created).To(BeFalse())
		Expect(deleted).To(BeFalse())
		Expect(getPolicy().Finalizers).To(Equal([]string{finalizer}))
	})
})

var _ = Describe("Resource policy deferred patch",
	Label(testlabels.Controller, testlabels.API, testlabels.EnvTest), func() {
		var ctx *builder.IntegrationTestContext

		BeforeEach(func() {
			ctx = suite.NewIntegrationTestContext()
		})
		AfterEach(func() {
			ctx.AfterEach()
		})

		DescribeTable("Persists provider status, metadata and spec changes",
			func(mode pkgcfg.MultiVC) {
				configCtx := pkgcfg.NewContext()
				pkgcfg.SetContext(configCtx, func(config *pkgcfg.Config) {
					config.MultiVC = mode
				})
				policy := &vmopv1.VirtualMachineSetResourcePolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "deferred-patch", Namespace: ctx.Namespace,
						Finalizers: []string{finalizer},
					},
					Spec: vmopv1.VirtualMachineSetResourcePolicySpec{Folder: "old-folder"},
				}
				key := ctrlclient.ObjectKeyFromObject(policy)
				Expect(ctx.Client.Create(ctx, policy)).To(Succeed())
				foreignPool := vmopv1.ResourcePoolStatus{
					ClusterMoID: "vc-1/domain-c1", ChildResourcePoolMoID: "resgroup-vc1",
				}
				foreignModule := vmopv1.VSphereClusterModuleStatus{
					ClusterMoID: "vc-1/domain-c1", GroupName: "cp", ModuleUuid: "module-vc1",
				}
				policy.Status.ResourcePools = []vmopv1.ResourcePoolStatus{foreignPool}
				policy.Status.ClusterModules = []vmopv1.VSphereClusterModuleStatus{foreignModule}
				Expect(ctx.Client.Status().Update(ctx, policy)).To(Succeed())

				ownPool := vmopv1.ResourcePoolStatus{
					ClusterMoID: "vc-2/domain-c1", ChildResourcePoolMoID: "resgroup-vc2",
				}
				ownModule := vmopv1.VSphereClusterModuleStatus{
					ClusterMoID: "vc-2/domain-c1", GroupName: "cp", ModuleUuid: "created-once-vc2",
				}
				newerForeignPool := vmopv1.ResourcePoolStatus{
					ClusterMoID: "vc-3/domain-c1", ChildResourcePoolMoID: "resgroup-vc3",
				}
				newerForeignModule := vmopv1.VSphereClusterModuleStatus{
					ClusterMoID: "vc-3/domain-c1", GroupName: "cp", ModuleUuid: "module-vc3",
				}
				providerCalls := 0
				provider := providerfake.NewVMProvider()
				provider.CreateOrUpdateVirtualMachineSetResourcePolicyFn = func(providerCtx context.Context,
					obj *vmopv1.VirtualMachineSetResourcePolicy) error {
					providerCalls++
					if mode.IsShard() {
						// Another shard publishes after this reconcile's initial read.
						latest := &vmopv1.VirtualMachineSetResourcePolicy{}
						Expect(ctx.Client.Get(providerCtx, key, latest)).To(Succeed())
						latest.Status.ResourcePools = append(latest.Status.ResourcePools, newerForeignPool)
						latest.Status.ClusterModules = append(latest.Status.ClusterModules, newerForeignModule)
						Expect(ctx.Client.Status().Update(providerCtx, latest)).To(Succeed())
					}
					obj.Annotations = map[string]string{"provider": "reconciled"}
					obj.Spec.Folder = "new-folder"
					obj.Status.ResourcePools = append(obj.Status.ResourcePools, ownPool)
					obj.Status.ClusterModules = append(obj.Status.ClusterModules, ownModule)
					return nil
				}
				reconciler := virtualmachinesetresourcepolicy.NewReconciler(
					configCtx, ctx.Client, logr.Discard(), provider)
				reconciler.APIReader = ctx.Client
				_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
				Expect(providerCalls).To(Equal(1))

				latest := &vmopv1.VirtualMachineSetResourcePolicy{}
				Expect(ctx.Client.Get(ctx, key, latest)).To(Succeed())
				Expect(latest.Annotations).To(HaveKeyWithValue("provider", "reconciled"))
				Expect(latest.Spec.Folder).To(Equal("new-folder"))
				Expect(latest.Finalizers).To(ContainElement(finalizer))
				expectedPools := []vmopv1.ResourcePoolStatus{foreignPool, ownPool}
				expectedModules := []vmopv1.VSphereClusterModuleStatus{foreignModule, ownModule}
				if mode.IsShard() {
					expectedPools = append(expectedPools, newerForeignPool)
					expectedModules = append(expectedModules, newerForeignModule)
				}
				Expect(latest.Status.ResourcePools).To(ConsistOf(expectedPools))
				Expect(latest.Status.ClusterModules).To(ConsistOf(expectedModules))
			},
			Entry("shard preserves concurrent foreign entries", pkgcfg.MultiVC{
				Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-2",
			}),
			Entry("combined retains the ordinary patch behavior", pkgcfg.MultiVC{}),
		)
	})
