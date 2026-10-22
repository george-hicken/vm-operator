// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivcshard_test

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vmware-tanzu/vm-operator/controllers/multivcshard"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

const (
	podNamespace   = "vmware-system-vmop"
	globalName     = "vmware-system-vmop-controller-manager"
	webhookCertDir = "/etc/vmware/wcp/webhook-certs"
)

func globalDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globalName,
			Namespace: podNamespace,
			UID:       "global-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrTo[int32](3),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"control-plane": "controller-manager"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"control-plane": "controller-manager",
						"name":          "vmoperator-controller-manager",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}},
					Containers: []corev1.Container{{
						Name:  "manager",
						Image: "vmware/vmop@sha256:abc",
						Args:  []string{"--metrics-addr=:9848", "--profiler-address=127.0.0.1:8073"},
						Env: []corev1.EnvVar{
							{Name: "DEPLOYMENT_NAME", Value: globalName},
							{Name: "LEADER_ELECTION_ID", Value: "vmop"},
						},
						Ports: []corev1.ContainerPort{
							{Name: "webhook-server", ContainerPort: 9878},
							{Name: "health-probe", ContainerPort: 9445},
							{Name: "metrics-server", ContainerPort: 9848},
						},
						LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
							Path: "/healthz", Port: intstr.FromString("health-probe"),
						}}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
							Path: "/readyz", Port: intstr.FromString("health-probe"),
						}}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "vmca-certs", MountPath: "/etc/vmware/wcp/tls"},
							{Name: "cert", MountPath: webhookCertDir},
							{Name: "client-ca", MountPath: webhookCertDir + "/client-ca"},
						},
					}},
					Volumes: []corev1.Volume{{Name: "vmca-certs"}, {Name: "cert"}, {Name: "client-ca"}},
				},
			},
		},
	}
}

func ptrTo[T any](v T) *T { return &v }

func testConfig() pkgcfg.Config {
	c := pkgcfg.Default()
	c.PodNamespace = podNamespace
	c.DeploymentName = globalName
	c.WebhookSecretVolumeMountPath = webhookCertDir
	c.MultiVC.Mode = pkgcfg.MultiVCModeGlobal
	return c
}

func envOf(c corev1.Container, name string) string {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func newSDDC(name string) *unstructured.Unstructured {
	obj := multivc.NewSDDC()
	obj.SetName(name)
	Expect(unstructured.SetNestedField(obj.Object, "https://"+name+".example.com", "spec", "endpointURL")).To(Succeed())
	return obj
}

func credentialSecret(sddc string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmop-" + sddc + "-xyz",
			Namespace: podNamespace,
			Labels: map[string]string{
				"vsphere-auth.vmware.com/vsphereserviceaccount": "vmop",
				"vsphere-auth.vmware.com/sddc":                  sddc,
			},
		},
	}
}

var _ = Describe("RenderDeployment", Label(testlabels.Controller), func() {
	It("Should derive a one-replica shard that serves no webhooks", func() {
		d, err := multivcshard.RenderDeployment(globalDeployment(), "vc-2", 1, testConfig())
		Expect(err).ToNot(HaveOccurred())

		Expect(d.Name).To(Equal("vmware-system-vmop-shard-vc-2"))
		Expect(*d.Spec.Replicas).To(Equal(int32(1)))
		Expect(d.Spec.Selector.MatchLabels).To(Equal(map[string]string{multivcshard.ShardLabelKey: "vc-2"}))
		Expect(d.Spec.Template.Labels).ToNot(HaveKey("control-plane"))
		Expect(d.Spec.Template.Labels["name"]).ToNot(Equal("vmoperator-controller-manager"))
		Expect(d.Spec.Template.Spec.Affinity).To(BeNil())
		Expect(d.OwnerReferences).To(HaveLen(1))
		Expect(d.OwnerReferences[0].Name).To(Equal(globalName))
		Expect(*d.OwnerReferences[0].Controller).To(BeTrue())

		c := d.Spec.Template.Spec.Containers[0]
		Expect(c.Image).To(Equal("vmware/vmop@sha256:abc"))
		Expect(envOf(c, "VMOP_MULTIVC_MODE")).To(Equal("shard"))
		Expect(envOf(c, "VMOP_SHARD_SDDC")).To(Equal("vc-2"))
		Expect(envOf(c, "VC_CREDS_SECRET_NAME")).To(Equal("wcp-vmop-sa-vc-auth-vc-2"))
		Expect(envOf(c, "DEPLOYMENT_NAME")).To(Equal(d.Name))
		Expect(envOf(c, "LEADER_ELECTION_ID")).To(Equal("vmop"))
		Expect(c.Args).To(ConsistOf("--metrics-addr=:18101", "--health-addr=:18001", "--profiler-address=0"))
		Expect(c.Ports).To(ConsistOf(
			corev1.ContainerPort{Name: "health-probe", ContainerPort: 18001},
			corev1.ContainerPort{Name: "metrics-server", ContainerPort: 18101}))
		Expect(c.VolumeMounts).To(ConsistOf(corev1.VolumeMount{Name: "vmca-certs", MountPath: "/etc/vmware/wcp/tls"}))
		Expect(d.Spec.Template.Spec.Volumes).To(ConsistOf(corev1.Volume{Name: "vmca-certs"}))
	})
	It("Should give each port index its own ports, and reject an index out of range", func() {
		a, err := multivcshard.RenderDeployment(globalDeployment(), "vc-1", 0, testConfig())
		Expect(err).ToNot(HaveOccurred())
		b, err := multivcshard.RenderDeployment(globalDeployment(), "vc-1", 2, testConfig())
		Expect(err).ToNot(HaveOccurred())
		Expect(a.Annotations[multivcshard.SpecHashAnnotationKey]).ToNot(Equal(b.Annotations[multivcshard.SpecHashAnnotationKey]))
		_, err = multivcshard.RenderDeployment(globalDeployment(), "vc-1", multivcshard.MaxShards, testConfig())
		Expect(err).To(HaveOccurred())
	})
	It("Should keep all shard listeners clear of observed stock host ports", func() {
		// Representative host ports from the run121 pod snapshot include the
		// runtime extension's 9460 health port and a separate 18312 webhook.
		stockHostPorts := map[int32]bool{
			9442: true, 9460: true, 9876: true, 18312: true,
			9445: true, 9848: true, 9878: true,
		}
		used := map[int32]bool{}
		for i := range multivcshard.MaxShards {
			d, err := multivcshard.RenderDeployment(globalDeployment(), "vc-1", i, testConfig())
			Expect(err).ToNot(HaveOccurred())
			Expect(d.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
			Expect(d.Annotations[multivcshard.PortIndexAnnotationKey]).To(Equal(strconv.Itoa(i)))
			c := d.Spec.Template.Spec.Containers[0]
			Expect(c.Ports).To(HaveLen(2))
			ports := map[string]int32{}
			for _, p := range c.Ports {
				Expect(p.ContainerPort).To(BeNumerically(">", 0))
				Expect(p.ContainerPort).To(BeNumerically("<=", 65535))
				Expect(stockHostPorts).ToNot(HaveKey(p.ContainerPort))
				Expect(used).ToNot(HaveKey(p.ContainerPort))
				used[p.ContainerPort] = true
				ports[p.Name] = p.ContainerPort
			}
			Expect(ports).To(HaveKey("health-probe"))
			Expect(ports).To(HaveKey("metrics-server"))
			Expect(c.Args).To(ContainElement("--health-addr=:" + strconv.Itoa(int(ports["health-probe"]))))
			Expect(c.Args).To(ContainElement("--metrics-addr=:" + strconv.Itoa(int(ports["metrics-server"]))))
			Expect(c.Args).To(ContainElement("--profiler-address=0"))
			Expect(c.LivenessProbe.HTTPGet.Port).To(Equal(intstr.FromString("health-probe")))
			Expect(c.ReadinessProbe.HTTPGet.Port).To(Equal(intstr.FromString("health-probe")))
			Expect(ports).ToNot(HaveKey("webhook-server"))
		}
		Expect(used).To(HaveLen(2 * multivcshard.MaxShards))
	})
})

type denyVerifier struct{}

func (denyVerifier) Verify(context.Context, *unstructured.Unstructured) error {
	return errors.New("not signed")
}

var _ = Describe("Reconcile", Label(testlabels.Controller), func() {
	var (
		ctx      context.Context
		client   ctrlclient.Client
		r        *multivcshard.Reconciler
		verifier multivc.EntitlementVerifier
		objs     []ctrlclient.Object
	)

	BeforeEach(func() {
		verifier = multivc.NewAllowAllVerifier(logr.Discard())
		objs = []ctrlclient.Object{globalDeployment(), newSDDC("vc-1"), newSDDC("vc-2"), credentialSecret("vc-2")}
	})

	JustBeforeEach(func() {
		ctx = pkgcfg.WithConfig(testConfig())
		client = builder.NewFakeClient(objs...)
		r = multivcshard.NewReconciler(ctx, client, logr.Discard(), verifier)
	})

	reconcile := func(name string) ctrl.Result {
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: ctrlclient.ObjectKey{Name: name}})
		Expect(err).ToNot(HaveOccurred())
		return res
	}
	getShard := func(name string) (*appsv1.Deployment, error) {
		d := &appsv1.Deployment{}
		err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: podNamespace, Name: "vmware-system-vmop-shard-" + name}, d)
		return d, err
	}
	getTemplate := func(name string) (*unstructured.Unstructured, error) {
		t := &unstructured.Unstructured{}
		t.SetGroupVersionKind(multivcshard.SecretTemplateGVK)
		err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: podNamespace, Name: "wcp-vmop-sa-vc-auth-" + name}, t)
		return t, err
	}

	It("Should render a shard and its credentials for every SDDC, the first included", func() {
		Expect(reconcile("vc-2")).To(Equal(ctrl.Result{}))
		d, err := getShard("vc-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(d.Annotations[multivcshard.PortIndexAnnotationKey]).To(Equal("0"))

		t, err := getTemplate("vc-2")
		Expect(err).ToNot(HaveOccurred())
		ref, _, _ := unstructured.NestedSlice(t.Object, "spec", "inputResources")
		Expect(ref).To(HaveLen(1))
		Expect(ref[0].(map[string]any)["ref"].(map[string]any)["name"]).To(Equal("vmop-vc-2-xyz"))

		By("waiting for the first SDDC's credential Secret", func() {
			Expect(reconcile("vc-1").RequeueAfter).To(BeNumerically(">", time.Duration(0)))
			d, err := getShard("vc-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(d.Annotations[multivcshard.PortIndexAnnotationKey]).To(Equal("1"))
		})
	})
	It("Should keep a shard's port index and not rewrite an unchanged shard", func() {
		reconcile("vc-2")
		before, err := getShard("vc-2")
		Expect(err).ToNot(HaveOccurred())
		reconcile("vc-1")
		reconcile("vc-2")
		after, err := getShard("vc-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
		Expect(after.Annotations[multivcshard.PortIndexAnnotationKey]).To(Equal("0"))

		Expect(client.Delete(ctx, newSDDC("vc-1"))).To(Succeed())
		reconcile("vc-1")
		reconcile("vc-2")
		afterRemoval, err := getShard("vc-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(afterRemoval.ResourceVersion).To(Equal(before.ResourceVersion))
		Expect(afterRemoval.Annotations[multivcshard.PortIndexAnnotationKey]).To(Equal("0"))
	})
	It("Should delete the shard when its SDDC is gone", func() {
		reconcile("vc-2")
		Expect(client.Delete(ctx, newSDDC("vc-2"))).To(Succeed())
		reconcile("vc-2")
		_, err := getShard("vc-2")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getTemplate("vc-2")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	When("the SDDC is not entitled", func() {
		BeforeEach(func() {
			verifier = denyVerifier{}
		})
		It("Should render no shard", func() {
			reconcile("vc-2")
			_, err := getShard("vc-2")
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})
