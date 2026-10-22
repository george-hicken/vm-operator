// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package vsphere_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vapi/library"
	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"

	imgregv1 "github.com/vmware-tanzu/image-registry-operator-api/api/v1alpha2"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	backupapi "github.com/vmware-tanzu/vm-operator/pkg/backup/api"
	"github.com/vmware-tanzu/vm-operator/pkg/conditions"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgconst "github.com/vmware-tanzu/vm-operator/pkg/constants"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	pkgerr "github.com/vmware-tanzu/vm-operator/pkg/errors"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	"github.com/vmware-tanzu/vm-operator/pkg/providers"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/virtualmachine"
	pkgutil "github.com/vmware-tanzu/vm-operator/pkg/util"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

func vmCreateTests() {
	var (
		parentCtx   context.Context
		initObjects []client.Object
		testConfig  builder.VCSimTestConfig
		ctx         *builder.TestContextForVCSim
		vmProvider  providers.VirtualMachineProviderInterface
		nsInfo      builder.WorkloadNamespaceInfo

		vm      *vmopv1.VirtualMachine
		vmClass *vmopv1.VirtualMachineClass
	)

	BeforeEach(func() {
		parentCtx = newVMTestParentContext()
		testConfig = newVMTestConfig()

		vmClass, vm = newVMTestObjects("test-vm")
	})

	JustBeforeEach(func() {
		ctx, vmProvider, nsInfo = setupVMTest(
			parentCtx, testConfig, vmClass, vm, initObjects...)

		pinVMToFirstZone(ctx, vm)
	})

	AfterEach(func() {
		vmTestAfterEach(ctx, vm)

		vmClass = nil
		vm = nil

		ctx = nil
		initObjects = nil
		vmProvider = nil
		nsInfo = builder.WorkloadNamespaceInfo{}
	})

	It("Basic VM", func() {
		vcVM, err := createOrUpdateAndGetVcVM(ctx, vmProvider, vm)
		Expect(err).ToNot(HaveOccurred())

		var o mo.VirtualMachine
		Expect(vcVM.Properties(ctx, vcVM.Reference(), nil, &o)).To(Succeed())

		By("has VC UUID annotation set", func() {
			Expect(vm.Annotations).Should(HaveKeyWithValue(vmopv1.ManagerID, ctx.VCClient.Client.ServiceContent.About.InstanceUuid))
		})

		By("has expected Status values", func() {
			Expect(vm.Status.PowerState).To(Equal(vm.Spec.PowerState))
			Expect(vm.Status.NodeName).ToNot(BeEmpty())
			Expect(vm.Status.InstanceUUID).To(And(Not(BeEmpty()), Equal(o.Config.InstanceUuid)))
			Expect(vm.Status.BiosUUID).To(And(Not(BeEmpty()), Equal(o.Config.Uuid)))

			Expect(vm.Status.Class).ToNot(BeNil())
			Expect(vm.Status.Class.Name).To(Equal(vm.Spec.ClassName))
			Expect(vm.Status.Class.APIVersion).To(Equal(vmopv1.GroupVersion.String()))

			Expect(conditions.IsTrue(vm, vmopv1.VirtualMachineConditionClassReady)).To(BeTrue())
			Expect(conditions.IsTrue(vm, vmopv1.VirtualMachineConditionImageReady)).To(BeTrue())
			Expect(conditions.IsTrue(vm, vmopv1.VirtualMachineConditionStorageReady)).To(BeTrue())
			Expect(conditions.IsTrue(vm, vmopv1.VirtualMachineConditionCreated)).To(BeTrue())

			By("did not have VMSetResourcePool", func() {
				Expect(vm.Spec.Reserved).To(BeNil())
				Expect(conditions.Has(vm, vmopv1.VirtualMachineConditionVMSetResourcePolicyReady)).To(BeFalse())
			})
			By("did not have Bootstrap", func() {
				Expect(vm.Spec.Bootstrap).To(BeNil())
				Expect(conditions.Has(vm, vmopv1.VirtualMachineConditionBootstrapReady)).To(BeFalse())
			})
			By("did not have Network", func() {
				Expect(vm.Spec.Network.Disabled).To(BeTrue())
				Expect(conditions.Has(vm, vmopv1.VirtualMachineConditionNetworkReady)).To(BeFalse())
			})
		})

		By("has expected inventory path", func() {
			Expect(vcVM.InventoryPath).To(HaveSuffix(fmt.Sprintf("/%s/%s", nsInfo.Namespace, vm.Name)))
		})

		By("has expected namespace resource pool", func() {
			rp, err := vcVM.ResourcePool(ctx)
			Expect(err).ToNot(HaveOccurred())
			nsRP := ctx.GetResourcePoolForNamespace(nsInfo.Namespace, "", "")
			Expect(nsRP).ToNot(BeNil())
			Expect(rp.Reference().Value).To(Equal(nsRP.Reference().Value))
		})

		By("has expected power state", func() {
			Expect(o.Summary.Runtime.PowerState).To(Equal(vimtypes.VirtualMachinePowerStatePoweredOn))
		})

		vmClassRes := &vmClass.Spec.Policies.Resources

		By("has expected CpuAllocation", func() {
			Expect(o.Config.CpuAllocation).ToNot(BeNil())

			reservation := o.Config.CpuAllocation.Reservation
			Expect(reservation).ToNot(BeNil())
			Expect(*reservation).To(Equal(virtualmachine.CPUQuantityToMhz(vmClassRes.Requests.Cpu, vcsimCPUFreq)))
			limit := o.Config.CpuAllocation.Limit
			Expect(limit).ToNot(BeNil())
			Expect(*limit).To(Equal(virtualmachine.CPUQuantityToMhz(vmClassRes.Limits.Cpu, vcsimCPUFreq)))
		})

		By("has expected MemoryAllocation", func() {
			Expect(o.Config.MemoryAllocation).ToNot(BeNil())

			reservation := o.Config.MemoryAllocation.Reservation
			Expect(reservation).ToNot(BeNil())
			Expect(*reservation).To(Equal(virtualmachine.MemoryQuantityToMb(vmClassRes.Requests.Memory)))
			limit := o.Config.MemoryAllocation.Limit
			Expect(limit).ToNot(BeNil())
			Expect(*limit).To(Equal(virtualmachine.MemoryQuantityToMb(vmClassRes.Limits.Memory)))
		})

		By("has expected hardware config", func() {
			Expect(o.Summary.Config.NumCpu).To(BeEquivalentTo(vmClass.Spec.Hardware.Cpus))
			Expect(o.Summary.Config.MemorySizeMB).To(BeEquivalentTo(vmClass.Spec.Hardware.Memory.Value() / 1024 / 1024))
		})

		By("has expected backup ExtraConfig key", func() {
			Expect(o.Config.ExtraConfig).ToNot(BeNil())

			ecMap := pkgutil.OptionValues(o.Config.ExtraConfig).StringMap()
			Expect(ecMap).To(HaveKey(backupapi.VMResourceYAMLExtraConfigKey))
		})

		// TODO: More assertions!
	})

	When("using async create", func() {
		BeforeEach(func() {
			pkgcfg.SetContext(parentCtx, func(config *pkgcfg.Config) {
				config.AsyncCreateEnabled = true
				config.AsyncSignalEnabled = true
			})
		})
		JustBeforeEach(func() {
			pkgcfg.SetContext(ctx, func(config *pkgcfg.Config) {
				config.MaxDeployThreadsOnProvider = 16
			})
		})

		It("should succeed", func() {
			Expect(createOrUpdateVM(ctx, vmProvider, vm)).To(Succeed())
			Expect(vm.Status.UniqueID).ToNot(BeEmpty())
		})

		When("there is an error getting the pre-reqs", func() {
			It("should not prevent a subsequent create attempt from going through", func() {
				imgName := vm.Spec.Image.Name
				vm.Spec.Image.Name = "does-not-exist"
				err := createOrUpdateVM(ctx, vmProvider, vm)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(
					"clustervirtualmachineimages.vmoperator.vmware.com \"does-not-exist\" not found: " +
						"clustervirtualmachineimages.vmoperator.vmware.com \"does-not-exist\" not found"))
				vm.Spec.Image.Name = imgName
				Expect(createOrUpdateVM(ctx, vmProvider, vm)).To(Succeed())
				Expect(vm.Status.UniqueID).ToNot(BeEmpty())
			})
		})

		When("there is an error creating the VM", func() {
			JustBeforeEach(func() {
				ctx.SimulatorContext().Map.Handler = func(
					ctx *simulator.Context,
					m *simulator.Method) (mo.Reference, vimtypes.BaseMethodFault) {

					// Fault whichever call the configured deploy path
					// makes: Fast Deploy creates the VM itself, while the
					// content library path imports the OVF.
					switch m.Name {
					case "CreateVM_Task", "ImportVApp":
						return nil, &vimtypes.InvalidRequest{}
					}
					return nil, nil
				}
			})

			It("should fail to create the VM without an NPE", func() {
				err := createOrUpdateVM(ctx, vmProvider, vm)
				Expect(err).To(HaveOccurred())
				Eventually(func(g Gomega) {
					g.Expect(ctx.Client.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
					g.Expect(vm.Status.UniqueID).To(BeEmpty())
					c := conditions.Get(vm, vmopv1.VirtualMachineConditionCreated)
					g.Expect(c).ToNot(BeNil())
					g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(c.Reason).To(Equal("Error"))
					g.Expect(c.Message).To(Equal(
						"failed to call create task: ServerFaultCode: InvalidRequest"))
				}).Should(Succeed())
			})
		})
	})

	Describe("FastDeploy", Label(testlabels.Create), vmFastDeployTests)

}

var _ = Describe("Local image cache bootstrap", Label(testlabels.VCSim, testlabels.Create), func() {
	var (
		ctx, source *builder.TestContextForVCSim
		provider    providers.VirtualMachineProviderInterface
		vm          *vmopv1.VirtualMachine
		image       *vmopv1.ClusterVirtualMachineImage
		sourceCache *vmopv1.VirtualMachineImageCache
		localKey    client.ObjectKey
	)

	BeforeEach(func() {
		parent := newVMTestParentContext()
		source = suite.NewTestContextForVCSimWithParentContext(newVMTestParentContext(), newVMTestConfig())
		sourceCache = source.MarkImageCacheReady(source.ContentLibraryItem1Cache).DeepCopy()
		vmClass, obj := newVMTestObjects("local-cache-vm")
		vm = obj
		ctx, provider, _ = setupVMTest(parent, newVMTestConfig(), vmClass, vm)
		pinVMToFirstZone(ctx, vm)
		pkgcfg.SetContext(ctx, func(config *pkgcfg.Config) {
			config.MultiVC = pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "local", OwnedZones: ctx.GetFirstZoneName()}
		})
		trust, err := os.ReadFile(ctx.VCClientConfig.CAFilePath)
		Expect(err).ToNot(HaveOccurred())
		sddc := multivc.NewSDDC()
		sddc.SetName("local")
		sddc.SetAnnotations(map[string]string{
			multivc.AnnotationVCenterUUID: ctx.VCClient.ServiceContent.About.InstanceUuid,
			multivc.AnnotationDatacenter:  ctx.Datacenter.Reference().Value,
		})
		Expect(unstructured.SetNestedField(sddc.Object, "https://"+net.JoinHostPort(ctx.VCClientConfig.Host, ctx.VCClientConfig.Port), "spec", "endpointURL")).To(Succeed())
		Expect(unstructured.SetNestedField(sddc.Object, string(trust), "spec", "trustBundle")).To(Succeed())
		Expect(ctx.Client.Create(ctx, sddc)).To(Succeed())
		Expect(ctx.Client.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "wcp-cluster-config"},
			Data:       map[string]string{"wcp-cluster-config.yaml": "supervisor_id: supervisor-a\n"},
		})).To(Succeed())
		image = resolveVMTestImage(ctx, newVMTestConfig())
		localKey = client.ObjectKey{Namespace: pkgcfg.FromContext(ctx).PodNamespace, Name: pkgutil.VMIName(ctx.ContentLibraryItem1ID)}
		Expect(ctx.Client.Delete(ctx, &vmopv1.VirtualMachineImageCache{ObjectMeta: metav1.ObjectMeta{Namespace: localKey.Namespace, Name: localKey.Name}})).To(Succeed())
		itemRef := &imgregv1.ClusterContentLibraryItem{
			ObjectMeta: metav1.ObjectMeta{Name: "source-library-item"},
			Spec:       imgregv1.ContentLibraryItemSpec{ID: source.ContentLibraryItem1ID, LibraryName: "source-library"},
		}
		Expect(ctx.Client.Create(ctx, itemRef)).To(Succeed())
		image.Spec.ProviderRef.Name = itemRef.Name
		Expect(ctx.Client.Update(ctx, image)).To(Succeed())
		image.Status.ProviderItemID = source.ContentLibraryItem1ID
		image.Status.ProviderContentVersion = "source-version"
		item, err := library.NewManager(ctx.RestClient).GetLibraryItem(ctx, ctx.ContentLibraryItem1ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(library.NewManager(ctx.RestClient).UpdateLibraryItem(ctx, &library.Item{ID: item.ID, Name: "local-cache-image"})).To(Succeed())
		image.Status.Name = "local-cache-image"
		Expect(ctx.Client.Status().Update(ctx, image)).To(Succeed())
		sourceCache.ResourceVersion = ""
		sourceCache.UID = ""
		Expect(ctx.Client.Create(ctx, sourceCache)).To(Succeed())
		Expect(ctx.ContentLibraryItem1ID).ToNot(Equal(source.ContentLibraryItem1ID))
	})

	AfterEach(func() {
		ctx.AfterEach()
		source.AfterEach()
	})

	assertSourceUnchanged := func() {
		var got vmopv1.VirtualMachineImageCache
		Expect(ctx.Client.Get(ctx, client.ObjectKeyFromObject(sourceCache), &got)).To(Succeed())
		Expect(got).To(Equal(*sourceCache))
		var gotImage vmopv1.ClusterVirtualMachineImage
		Expect(ctx.Client.Get(ctx, client.ObjectKeyFromObject(image), &gotImage)).To(Succeed())
		Expect(gotImage.Status.ProviderItemID).To(Equal(source.ContentLibraryItem1ID))
		Expect(gotImage.Status.ProviderContentVersion).To(Equal("source-version"))
	}

	It("creates a local hardware prerequisite before the first blocking read", func() {
		err := provider.CreateOrUpdateVirtualMachine(ctx, vm)
		var pending pkgerr.VMICacheNotReadyError
		Expect(errors.As(err, &pending)).To(BeTrue(), "provider error: %v", err)
		Expect(pending.Name).To(Equal(localKey.Name))
		var got vmopv1.VirtualMachineImageCache
		Expect(ctx.Client.Get(ctx, localKey, &got)).To(Succeed())
		Expect(got.Spec.ProviderID).To(Equal(ctx.ContentLibraryItem1ID))
		Expect(got.Spec.ProviderVersion).To(Equal(ctx.ContentLibraryItem1Version))
		Expect(got.Status.Conditions).To(BeEmpty())
		Expect(got.Spec.Locations).To(BeEmpty())
		assertSourceUnchanged()
	})

	It("recovers a missing labelled cache without a VM operation and preserves it on repeat", func() {
		vm.Labels[pkgconst.VMICacheLabelKey] = localKey.Name
		vm.Status.UniqueID = "vm-existing"
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(Succeed())
		var before, after vmopv1.VirtualMachineImageCache
		Expect(ctx.Client.Get(ctx, localKey, &before)).To(Succeed())
		before.AddLocation("datacenter-local", "datastore-local", "profile-local")
		Expect(ctx.Client.Update(ctx, &before)).To(Succeed())
		conditions.MarkTrue(&before, vmopv1.VirtualMachineImageCacheConditionHardwareReady)
		Expect(ctx.Client.Status().Update(ctx, &before)).To(Succeed())
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(Succeed())
		Expect(ctx.Client.Get(ctx, localKey, &after)).To(Succeed())
		Expect(after).To(Equal(before))
		Expect(conditions.IsTrue(after, vmopv1.VirtualMachineImageCacheConditionHardwareReady)).To(BeTrue())
		Expect(after.Spec.Locations).To(HaveLen(1))
		Expect(vm.Status.UniqueID).To(Equal("vm-existing"))
		assertSourceUnchanged()
	})

	It("refuses a waiting label that does not identify the resolved local item", func() {
		vm.Labels[pkgconst.VMICacheLabelKey] = sourceCache.Name
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(MatchError(ContainSubstring("does not match resolved image cache")))
		var got vmopv1.VirtualMachineImageCache
		Expect(apierrors.IsNotFound(ctx.Client.Get(ctx, localKey, &got))).To(BeTrue())
		assertSourceUnchanged()
	})

	It("does not create a cache when the local catalog item has the wrong type", func() {
		mgr := library.NewManager(ctx.RestClient)
		Expect(mgr.DeleteLibraryItem(ctx, &library.Item{ID: ctx.ContentLibraryItem1ID})).To(Succeed())
		_, err := mgr.CreateLibraryItem(ctx, library.Item{LibraryID: ctx.LocalContentLibraryID, Name: image.Status.Name, Type: "iso"})
		Expect(err).ToNot(HaveOccurred())
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(MatchError(ContainSubstring("does not match image")))
		var got vmopv1.VirtualMachineImageCache
		Expect(apierrors.IsNotFound(ctx.Client.Get(ctx, localKey, &got))).To(BeTrue())
		assertSourceUnchanged()
	})

	It("uses the existing local identity when the image already belongs to this vCenter", func() {
		image.Status.ProviderItemID = ctx.ContentLibraryItem1ID
		image.Status.ProviderContentVersion = ctx.ContentLibraryItem1Version
		Expect(ctx.Client.Status().Update(ctx, image)).To(Succeed())
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(Succeed())
		var got vmopv1.VirtualMachineImageCache
		Expect(ctx.Client.Get(ctx, localKey, &got)).To(Succeed())
		Expect(got.Spec.ProviderID).To(Equal(ctx.ContentLibraryItem1ID))
		Expect(got.Spec.ProviderVersion).To(Equal(ctx.ContentLibraryItem1Version))
		Expect(got.Status.Conditions).To(BeEmpty())
	})

	It("does not choose between ambiguous local items", func() {
		_, err := library.NewManager(ctx.RestClient).CreateLibraryItem(ctx, library.Item{
			LibraryID: ctx.LocalContentLibraryID, Name: image.Status.Name, Type: "ovf",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(MatchError(ContainSubstring("found 2 library items")))
		var got vmopv1.VirtualMachineImageCache
		Expect(apierrors.IsNotFound(ctx.Client.Get(ctx, localKey, &got))).To(BeTrue())
		assertSourceUnchanged()
	})

	It("does not create a cache when no matching local catalog item exists", func() {
		image.Status.Name = "absent-local-image"
		Expect(ctx.Client.Status().Update(ctx, image)).To(Succeed())
		Expect(provider.EnsureVirtualMachineImageCache(ctx, vm)).To(MatchError(ContainSubstring("found 0 library items")))
		var got vmopv1.VirtualMachineImageCache
		Expect(apierrors.IsNotFound(ctx.Client.Get(ctx, localKey, &got))).To(BeTrue())
		assertSourceUnchanged()
	})
})
