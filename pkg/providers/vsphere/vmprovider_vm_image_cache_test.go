// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package vsphere_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vmware/govmomi/object"
	vimtypes "github.com/vmware/govmomi/vim25/types"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	pkgerr "github.com/vmware-tanzu/vm-operator/pkg/errors"
	"github.com/vmware-tanzu/vm-operator/pkg/providers"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere"
	vcclient "github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/client"
	vcconfig "github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/config"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/vmlifecycle"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

var _ = Describe("VirtualMachineImageCache location", Label(testlabels.VCSim, testlabels.Create), func() {

	var (
		ctx *builder.TestContextForVCSim
	)

	AfterEach(func() {
		if ctx != nil {
			ctx.AfterEach()
		}
		ctx = nil
	})

	It("is keyed on the datacenter of a non-primary vCenter client", func() {
		var (
			vmProvider providers.VirtualMachineProviderInterface
			nsInfo     builder.WorkloadNamespaceInfo
		)
		ctx, vmProvider, nsInfo = newVMTestContext(
			newVMTestParentContext(), builder.VCSimTestConfig{})

		// A second datacenter stands in for the datacenter of a non-primary vCenter.
		dc2, err := object.NewRootFolder(ctx.VCClient.Client).CreateDatacenter(ctx, "dc2")
		Expect(err).ToNot(HaveOccurred())

		providerCfg, err := vcconfig.GetProviderConfig(ctx, ctx.Client)
		Expect(err).ToNot(HaveOccurred())
		primary, err := vcclient.NewClient(ctx, providerCfg)
		Expect(err).ToNot(HaveOccurred())

		secondaryCfg := *providerCfg
		secondaryCfg.Datacenter = dc2.Reference().Value
		secondary, err := vcclient.NewClient(ctx, &secondaryCfg)
		Expect(err).ToNot(HaveOccurred())
		Expect(secondary.Datacenter().Reference().Value).ToNot(Equal(primary.Datacenter().Reference().Value))

		vm := &vmopv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vm-1",
				Namespace: nsInfo.Namespace,
			},
		}
		vmCtx := pkgctx.NewVirtualMachineContext(ctx, vm)

		createArgs := &vsphere.VMCreateArgs{}
		createArgs.Datastores = []vmlifecycle.DatastoreRef{
			{
				MoRef: vimtypes.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"},
			},
		}
		createArgs.ImageStatus.ProviderItemID = "item-1"
		createArgs.ImageStatus.ProviderContentVersion = "v1"

		err = vsphere.CreateGetSourceFilePathsForTest(
			vmProvider, primary, secondary, vmCtx, createArgs)

		var notReady pkgerr.VMICacheNotReadyError
		Expect(errors.As(err, &notReady)).To(BeTrue())
		Expect(notReady.DatacenterID).To(Equal(dc2.Reference().Value))
		Expect(notReady.DatastoreID).To(Equal("datastore-1"))
	})
})
