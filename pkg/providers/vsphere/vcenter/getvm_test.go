// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package vcenter_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	"k8s.io/apimachinery/pkg/types"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/constants"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/vcenter"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/vmidentity"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

func getVMTests() {
	Describe("GetVirtualMachine", getVM)
}

func getVM() {
	// Use a VM that vcsim creates for us.
	const vcVMName = "DC0_C0_RP0_VM0"

	var (
		ctx        *builder.TestContextForVCSim
		nsInfo     builder.WorkloadNamespaceInfo
		testConfig builder.VCSimTestConfig

		vmCtx pkgctx.VirtualMachineContext
	)

	BeforeEach(func() {
		testConfig = builder.VCSimTestConfig{}
		ctx = suite.NewTestContextForVCSim(testConfig)
		nsInfo = ctx.CreateWorkloadNamespace()

		vm := builder.DummyVirtualMachine()
		vm.Name = "getvm-test"
		vm.Namespace = nsInfo.Namespace

		vmCtx = pkgctx.VirtualMachineContext{
			Context: ctx,
			Logger:  suite.GetLogger().WithValues("vmName", vm.Name),
			VM:      vm,
		}
	})

	Context("Gets VM when MoID is set", func() {
		BeforeEach(func() {
			vm, err := ctx.Finder.VirtualMachine(ctx, vcVMName)
			Expect(err).ToNot(HaveOccurred())
			vmCtx.VM.Status.UniqueID = vm.Reference().Value
			var o mo.VirtualMachine
			Expect(vm.Properties(ctx, vm.Reference(), []string{"config.instanceUuid"}, &o)).To(Succeed())
			vmCtx.VM.Spec.InstanceUUID = o.Config.InstanceUuid
		})

		It("returns success", func() {
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
			Expect(vm.Reference().Value).To(Equal(vmCtx.VM.Status.UniqueID))
		})

		Context("VC client is logged out", func() {
			BeforeEach(func() {
				Expect(ctx.VCClient.Logout(ctx)).To(Succeed())
			})

			It("returns error", func() {
				vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
				Expect(err).To(HaveOccurred())
				Expect(vm).To(BeNil())
			})
		})
	})

	Context("Supervisor stamp", func() {
		const ourID = "supervisor-a"

		var vcVM *object.VirtualMachine

		stamp := func(id string) {
			task, err := vcVM.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{
				ExtraConfig: []vimtypes.BaseOptionValue{
					&vimtypes.OptionValue{Key: vmidentity.ExtraConfigSupervisorID, Value: id},
					&vimtypes.OptionValue{Key: constants.ExtraConfigVMServiceNamespacedName, Value: vmCtx.VM.NamespacedName()},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())
		}

		BeforeEach(func() {
			var err error
			vcVM, err = ctx.Finder.VirtualMachine(ctx, vcVMName)
			Expect(err).ToNot(HaveOccurred())
			var o mo.VirtualMachine
			Expect(vcVM.Properties(ctx, vcVM.Reference(), []string{"config.instanceUuid"}, &o)).To(Succeed())
			vmCtx.VM.Status.UniqueID = vcVM.Reference().Value
			vmCtx.VM.Spec.InstanceUUID = o.Config.InstanceUuid
			vmCtx.Context = vmidentity.WithContext(vmCtx.Context, vmidentity.Identity{SupervisorID: ourID})
		})

		It("adopts a VM stamped by this Supervisor", func() {
			stamp(ourID)
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
			Expect(vm.Reference().Value).To(Equal(vcVM.Reference().Value))
		})

		It("adopts a VM with no stamp", func() {
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
		})

		It("does not adopt a VM stamped by another Supervisor by MoID or instance UUID", func() {
			stamp("supervisor-b")
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
			Expect(vm).To(BeNil())
		})

		It("rejects a foreign namespace and organization on both lookup paths", func() {
			task, err := vcVM.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{
				ExtraConfig: []vimtypes.BaseOptionValue{
					&vimtypes.OptionValue{Key: vmidentity.ExtraConfigSupervisorID, Value: ourID},
					&vimtypes.OptionValue{Key: constants.ExtraConfigVMServiceNamespacedName, Value: "other/vm"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())
			for _, moID := range []string{vcVM.Reference().Value, ""} {
				vmCtx.VM.Status.UniqueID = moID
				vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
				Expect(vm).To(BeNil())
				Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
			}

			task, err = vcVM.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{
				ExtraConfig: []vimtypes.BaseOptionValue{
					&vimtypes.OptionValue{Key: constants.ExtraConfigVMServiceNamespacedName, Value: vmCtx.VM.NamespacedName()},
					&vimtypes.OptionValue{Key: vmidentity.ExtraConfigNamespaceOrgID, Value: "foreign-org"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(vm).To(BeNil())
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
		})

		It("rejects a stamped VM without a namespace/name", func() {
			task, err := vcVM.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{
				ExtraConfig: []vimtypes.BaseOptionValue{
					&vimtypes.OptionValue{Key: vmidentity.ExtraConfigSupervisorID, Value: ourID},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(vm).To(BeNil())
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
		})

		It("stamps an explicit import before ordinary shard lookup may use it", func() {
			vmCtx.Context = vmidentity.WithContext(vmCtx.Context,
				vmidentity.Identity{SupervisorID: ourID, OrgID: "org-a"})
			pkgcfg.SetContext(vmCtx.Context, func(config *pkgcfg.Config) {
				config.MultiVC.Mode = pkgcfg.MultiVCModeShard
			})
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(vm).To(BeNil())
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
			vmCtx.VM.Annotations = map[string]string{vmopv1.ImportedVMAnnotation: ""}
			vm, err = vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(vm).To(BeNil())
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
			vmCtx.Context = vmidentity.WithAdoption(vmCtx.Context)
			vm, err = vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
			var observed mo.VirtualMachine
			Expect(vcVM.Properties(ctx, vcVM.Reference(), []string{"config.extraConfig"}, &observed)).To(Succeed())
			Expect(observed.Config).ToNot(BeNil())
			stamps := object.OptionValueList(observed.Config.ExtraConfig).StringMap()
			Expect(stamps).To(HaveKeyWithValue(vmidentity.ExtraConfigSupervisorID, ourID))
			Expect(stamps).To(HaveKeyWithValue(constants.ExtraConfigVMServiceNamespacedName, vmCtx.VM.NamespacedName()))
			Expect(stamps).To(HaveKeyWithValue(vmidentity.ExtraConfigNamespaceOrgID, "org-a"))
			vmCtx.Context = vmidentity.WithContext(ctx, vmidentity.Identity{SupervisorID: ourID, OrgID: "org-a"})
			pkgcfg.SetContext(vmCtx.Context, func(config *pkgcfg.Config) {
				config.MultiVC.Mode = pkgcfg.MultiVCModeShard
			})
			vm, err = vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
		})

		It("refuses to replace a legacy VM's conflicting organization stamp", func() {
			task, err := vcVM.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{
				ExtraConfig: []vimtypes.BaseOptionValue{
					&vimtypes.OptionValue{Key: vmidentity.ExtraConfigNamespaceOrgID, Value: "foreign-org"},
				},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())
			vmCtx.Context = vmidentity.WithAdoption(vmidentity.WithContext(vmCtx.Context,
				vmidentity.Identity{SupervisorID: ourID, OrgID: "our-org"}))
			vmCtx.VM.Annotations = map[string]string{vmopv1.RestoredVMAnnotation: ""}
			pkgcfg.SetContext(vmCtx.Context, func(config *pkgcfg.Config) {
				config.MultiVC.Mode = pkgcfg.MultiVCModeShard
			})
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(vm).To(BeNil())
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
			var observed mo.VirtualMachine
			Expect(vcVM.Properties(ctx, vcVM.Reference(), []string{"config.extraConfig"}, &observed)).To(Succeed())
			stamps := object.OptionValueList(observed.Config.ExtraConfig).StringMap()
			Expect(stamps).To(HaveKeyWithValue(vmidentity.ExtraConfigNamespaceOrgID, "foreign-org"))
			Expect(stamps).NotTo(HaveKey(vmidentity.ExtraConfigSupervisorID))
		})

		It("does not adopt through MoID when the instance UUID is ambiguous", func() {
			other, err := ctx.Finder.VirtualMachine(ctx, "DC0_C0_RP0_VM1")
			Expect(err).ToNot(HaveOccurred())
			task, err := other.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{
				InstanceUuid: vmCtx.VM.Spec.InstanceUUID,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())
			vmCtx.Context = vmidentity.WithAdoption(vmCtx.Context)
			vmCtx.VM.Annotations = map[string]string{vmopv1.ImportedVMAnnotation: ""}
			pkgcfg.SetContext(vmCtx.Context, func(config *pkgcfg.Config) {
				config.MultiVC.Mode = pkgcfg.MultiVCModeShard
			})
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(vm).To(BeNil())
			Expect(errors.Is(err, vcenter.ErrVMIdentityMismatch)).To(BeTrue())
			var observed mo.VirtualMachine
			Expect(vcVM.Properties(ctx, vcVM.Reference(), []string{"config.extraConfig"}, &observed)).To(Succeed())
			Expect(object.OptionValueList(observed.Config.ExtraConfig).StringMap()).NotTo(
				HaveKey(vmidentity.ExtraConfigSupervisorID))
		})
	})

	Context("MoID names a VM with another instance UUID", func() {
		// This is what a VM whose status was written in another vCenter
		// looks like: MoIDs repeat across vCenters, so the MoID can name an
		// unrelated VM here.
		const otherVCVMName = "DC0_C0_RP0_VM1"

		var wantMoID string

		BeforeEach(func() {
			vm, err := ctx.Finder.VirtualMachine(ctx, vcVMName)
			Expect(err).ToNot(HaveOccurred())
			vmCtx.VM.Status.UniqueID = vm.Reference().Value

			other, err := ctx.Finder.VirtualMachine(ctx, otherVCVMName)
			Expect(err).ToNot(HaveOccurred())
			var o mo.VirtualMachine
			Expect(other.Properties(ctx, other.Reference(), []string{"config.instanceUuid"}, &o)).To(Succeed())
			vmCtx.VM.Spec.InstanceUUID = o.Config.InstanceUuid
			wantMoID = other.Reference().Value
		})

		It("returns the VM with the matching instance UUID, not the MoID's", func() {
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
			Expect(vm.Reference().Value).To(Equal(wantMoID))
			Expect(vm.Reference().Value).ToNot(Equal(vmCtx.VM.Status.UniqueID))
		})

		When("no VM has the instance UUID", func() {
			BeforeEach(func() {
				vmCtx.VM.Spec.InstanceUUID = "00000000-0000-0000-0000-00000000beef"
				vmCtx.VM.UID = ""
			})

			It("returns no VM", func() {
				vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
				Expect(err).ToNot(HaveOccurred())
				Expect(vm).To(BeNil())
			})
		})
	})

	Context("Gets VM by UUID from Spec", func() {
		BeforeEach(func() {
			vm, err := ctx.Finder.VirtualMachine(ctx, vcVMName)
			Expect(err).ToNot(HaveOccurred())

			var o mo.VirtualMachine
			Expect(vm.Properties(ctx, vm.Reference(), nil, &o)).To(Succeed())
			vmCtx.VM.Spec.InstanceUUID = o.Config.InstanceUuid
		})

		It("returns success", func() {
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
		})

		Context("VC client is logged out", func() {
			BeforeEach(func() {
				Expect(ctx.VCClient.Logout(ctx)).To(Succeed())
			})

			It("returns error", func() {
				vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
				Expect(err).To(HaveOccurred())
				Expect(vm).To(BeNil())
			})
		})

		Context("Multiple VMs exist with same instanced UUID", func() {
			const vcVMName2 = "DC0_C0_RP0_VM1"

			BeforeEach(func() {
				vm, err := ctx.Finder.VirtualMachine(ctx, vcVMName2)
				Expect(err).ToNot(HaveOccurred())

				var o mo.VirtualMachine
				Expect(vm.Properties(ctx, vm.Reference(), nil, &o)).To(Succeed())
				task, err := vm.Reconfigure(ctx, vimtypes.VirtualMachineConfigSpec{InstanceUuid: vmCtx.VM.Spec.InstanceUUID})
				Expect(err).ToNot(HaveOccurred())
				Expect(task.Wait(ctx)).To(Succeed())
			})

			It("returns error", func() {
				vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(HavePrefix("found multiple VMs for instance UUID"))
				Expect(vm).To(BeNil())
			})
		})
	})

	Context("Gets VM by UUID from Metadata", func() {
		BeforeEach(func() {
			vm, err := ctx.Finder.VirtualMachine(ctx, vcVMName)
			Expect(err).ToNot(HaveOccurred())

			var o mo.VirtualMachine
			Expect(vm.Properties(ctx, vm.Reference(), nil, &o)).To(Succeed())
			vmCtx.VM.UID = types.UID(o.Config.InstanceUuid)
		})

		It("returns success", func() {
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).ToNot(BeNil())
		})

		It("does not fall back to the UID when the spec names another instance UUID", func() {
			vmCtx.VM.Spec.InstanceUUID = "00000000-0000-0000-0000-00000000beef"
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).To(BeNil())
		})
	})

	Context("VM does not exist", func() {
		BeforeEach(func() {
			vmCtx.VM.Spec.InstanceUUID = "bogus-uid"
			vmCtx.VM.Status.UniqueID = "bogus-moid"
		})

		It("returns success with nil vm", func() {
			vm, err := vcenter.GetVirtualMachine(vmCtx, ctx.VCClient.Client, ctx.Datacenter)
			Expect(err).ToNot(HaveOccurred())
			Expect(vm).To(BeNil())
		})
	})
}
