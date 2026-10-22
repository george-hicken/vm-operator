// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package virtualmachinesetresourcepolicy_test

import (
	"context"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	"github.com/vmware-tanzu/vm-operator/controllers/virtualmachinesetresourcepolicy"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	providerfake "github.com/vmware-tanzu/vm-operator/pkg/providers/fake"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

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
