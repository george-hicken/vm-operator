// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

var _ = Describe("Config", func() {
	Describe("Default", func() {
		It("Should bind the profiler to loopback only", func() {
			Expect(pkgcfg.Default().ProfilerAddr).To(Equal("127.0.0.1:8073"))
		})
	})

	Describe("GetMaxDeployThreadsOnProvider", func() {
		When("MaxDeployThreadsOnProvider == 0", func() {
			It("Should return MaxDeployThreadsOnProvider", func() {
				config := pkgcfg.Config{MaxDeployThreadsOnProvider: 100}
				Expect(config.GetMaxDeployThreadsOnProvider()).To(Equal(100))
			})
		})
		When("MaxDeployThreadsOnProvider > 0", func() {
			It("Should return percentage of MaxConcurrentReconciles / MaxCreateVMsOnProvider", func() {
				config := pkgcfg.Config{
					MaxDeployThreadsOnProvider: 0,
					MaxConcurrentReconciles:    20,
					MaxCreateVMsOnProvider:     80,
				}
				Expect(config.GetMaxDeployThreadsOnProvider()).To(Equal(16))
			})
		})
	})

	Describe("MultiVC", func() {
		It("Should default to combined mode, which owns every zone", func() {
			m := pkgcfg.Default().MultiVC
			Expect(m.Mode).To(Equal(pkgcfg.MultiVCModeCombined))
			Expect(m.RunsWebhooks()).To(BeTrue())
			Expect(m.HasVCClient()).To(BeTrue())
			Expect(m.OwnsZone("any")).To(BeTrue())
		})
		It("Should own every zone in global mode, and create no vCenter client", func() {
			m := pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal}
			Expect(m.IsGlobal()).To(BeTrue())
			Expect(m.RunsWebhooks()).To(BeTrue())
			Expect(m.HasVCClient()).To(BeFalse())
			Expect(m.OwnsZone("any")).To(BeTrue())
		})
		It("Should own only its zones in a shard when several SDDCs exist", func() {
			m := pkgcfg.MultiVC{
				Mode:       pkgcfg.MultiVCModeShard,
				ShardSDDC:  "sddc-2",
				OwnedZones: "zone-2a,zone-2b",
			}
			Expect(m.RunsWebhooks()).To(BeFalse())
			Expect(m.HasVCClient()).To(BeTrue())
			Expect(m.OwnsZone("zone-2a")).To(BeTrue())
			Expect(m.OwnsZone("zone-1")).To(BeFalse())
			Expect(m.OwnsZone("unowned")).To(BeFalse())
		})
		It("Should own every zone in the sole shard", func() {
			m := pkgcfg.MultiVC{
				Mode:       pkgcfg.MultiVCModeShard,
				ShardSDDC:  "sddc-1",
				Sole:       true,
				OwnedZones: "zone-1",
			}
			Expect(m.OwnsZone("zone-1")).To(BeTrue())
			Expect(m.OwnsZone("unowned")).To(BeTrue())
		})
	})
})
