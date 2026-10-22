// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"net"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	vcclient "github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/client"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/config"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

func multiVCTests() {
	var (
		ctx *builder.TestContextForVCSim
	)

	JustBeforeEach(func() {
		ctx = suite.NewTestContextForVCSim(builder.VCSimTestConfig{})
	})

	AfterEach(func() {
		ctx.AfterEach()
		ctx = nil
	})

	setMultiVC := func(m pkgcfg.MultiVC) {
		pkgcfg.SetContext(ctx, func(config *pkgcfg.Config) {
			config.MultiVC = m
		})
	}

	createSDDC := func(name, vcenterUUID string) {
		trust, err := os.ReadFile(ctx.VCClientConfig.CAFilePath)
		Expect(err).ToNot(HaveOccurred())

		obj := multivc.NewSDDC()
		obj.SetName(name)
		obj.SetAnnotations(map[string]string{
			multivc.AnnotationVCenterUUID: vcenterUUID,
			multivc.AnnotationDatacenter:  ctx.Datacenter.Reference().Value,
		})
		endpoint := "https://" + net.JoinHostPort(ctx.VCClientConfig.Host, ctx.VCClientConfig.Port)
		Expect(unstructured.SetNestedField(obj.Object, endpoint, "spec", "endpointURL")).To(Succeed())
		Expect(unstructured.SetNestedField(obj.Object, string(trust), "spec", "trustBundle")).To(Succeed())
		Expect(ctx.Client.Create(ctx, obj)).To(Succeed())
	}

	Describe("GetProviderConfig in the global mode", func() {
		It("Returns ErrNoVCClient", func() {
			setMultiVC(pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal})
			_, err := config.GetProviderConfig(ctx, ctx.Client)
			Expect(err).To(MatchError(config.ErrNoVCClient))
		})
	})

	Describe("GetProviderConfig in the shard mode", func() {
		JustBeforeEach(func() {
			setMultiVC(pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeShard, ShardSDDC: "vc-2"})
		})

		It("Takes the endpoint, trust, datacenter and identity from the SDDC", func() {
			uuid := ctx.VCClient.ServiceContent.About.InstanceUuid
			createSDDC("vc-2", uuid)

			providerConfig, err := config.GetProviderConfig(ctx, ctx.Client)
			Expect(err).ToNot(HaveOccurred())
			Expect(providerConfig.VcPNID).To(Equal(ctx.VCClientConfig.Host))
			Expect(providerConfig.VcPort).To(Equal(ctx.VCClientConfig.Port))
			Expect(providerConfig.Datacenter).To(Equal(ctx.Datacenter.Reference().Value))
			Expect(providerConfig.VCenterUUID).To(Equal(uuid))
			Expect(providerConfig.CAFilePath).To(BeEmpty())
			Expect(providerConfig.CAPEM).ToNot(BeEmpty())

			By("connecting with the SDDC's trust bundle", func() {
				c, err := vcclient.NewClient(ctx, providerConfig)
				Expect(err).ToNot(HaveOccurred())
				c.Logout(ctx)
			})
		})

		It("Refuses a vCenter whose instance UUID is not the SDDC's", func() {
			createSDDC("vc-2", "00000000-0000-0000-0000-000000000002")

			providerConfig, err := config.GetProviderConfig(ctx, ctx.Client)
			Expect(err).ToNot(HaveOccurred())
			_, err = vcclient.NewClient(ctx, providerConfig)
			Expect(err).To(MatchError(vcclient.ErrVCenterMismatch))
		})

		It("Fails when the shard's SDDC does not exist", func() {
			_, err := config.GetProviderConfig(ctx, ctx.Client)
			Expect(err).To(HaveOccurred())
		})
	})
}
