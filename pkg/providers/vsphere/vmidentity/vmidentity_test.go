// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package vmidentity_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/vmidentity"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

var _ = Describe("VM identity", func() {
	ctx := context.Background()

	It("Should read the Supervisor ID from wcp-cluster-config", func() {
		c := builder.NewFakeClient(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "wcp-cluster-config"},
			Data: map[string]string{
				"wcp-cluster-config.yaml": "vc_pnid: vc.example.com\nsupervisor_id: sv-123\n",
			},
		})
		id, err := vmidentity.GetSupervisorID(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(id).To(Equal("sv-123"))
	})
	It("Should return no Supervisor ID when the cluster config is missing", func() {
		id, err := vmidentity.GetSupervisorID(ctx, builder.NewFakeClient())
		Expect(err).ToNot(HaveOccurred())
		Expect(id).To(BeEmpty())
	})
	It("Should read the namespace organization ID label, if set", func() {
		c := builder.NewFakeClient(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "ns-org",
				Labels: map[string]string{vmidentity.NamespaceOrgIDLabel: "org-7"},
			}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-plain"}})
		org, err := vmidentity.GetNamespaceOrgID(ctx, c, "ns-org")
		Expect(err).ToNot(HaveOccurred())
		Expect(org).To(Equal("org-7"))
		org, err = vmidentity.GetNamespaceOrgID(ctx, c, "ns-plain")
		Expect(err).ToNot(HaveOccurred())
		Expect(org).To(BeEmpty())
	})
	It("Should carry the identity in a context", func() {
		id := vmidentity.Identity{SupervisorID: "sv-1", OrgID: "org-1"}
		Expect(vmidentity.FromContext(vmidentity.WithContext(ctx, id))).To(Equal(id))
		Expect(vmidentity.FromContext(ctx)).To(Equal(vmidentity.Identity{}))
	})
	It("Should match only this Supervisor's nonempty stamp", func() {
		Expect(vmidentity.SupervisorMatches("", "sv-1")).To(BeFalse())
		Expect(vmidentity.SupervisorMatches("sv-1", "sv-1")).To(BeTrue())
		Expect(vmidentity.SupervisorMatches("sv-2", "sv-1")).To(BeFalse())
		Expect(vmidentity.SupervisorMatches("sv-1", "")).To(BeFalse())
	})
	It("Should stamp only the identity fields that are set", func() {
		Expect(vmidentity.Identity{SupervisorID: "sv-1", OrgID: "org-1"}.ExtraConfig()).To(Equal(map[string]string{
			vmidentity.ExtraConfigSupervisorID:   "sv-1",
			vmidentity.ExtraConfigNamespaceOrgID: "org-1",
		}))
		Expect(vmidentity.Identity{SupervisorID: "sv-1"}.ExtraConfig()).To(Equal(map[string]string{
			vmidentity.ExtraConfigSupervisorID: "sv-1",
		}))
		Expect(vmidentity.Identity{}.ExtraConfig()).To(BeEmpty())
	})
})
