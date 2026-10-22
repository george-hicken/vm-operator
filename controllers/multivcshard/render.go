// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivcshard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	"github.com/vmware-tanzu/vm-operator/pkg/config/env"
	"github.com/vmware-tanzu/vm-operator/pkg/util/ptr"
)

const (
	// ShardLabelKey labels a shard's Deployment and pods with the name of the
	// shard's SDDC. It is on VM Operator's own workloads only.
	ShardLabelKey = "multivc.vmoperator.vmware.com/shard"

	// PortIndexAnnotationKey records the port index of a shard's Deployment,
	// so a shard keeps its ports when SDDCs are added or removed.
	PortIndexAnnotationKey = "multivc.vmoperator.vmware.com/port-index"

	// SDDCAnnotationKey records the name of a shard's SDDC on the shard's
	// Deployment. The label value may be a bounded form of the name.
	SDDCAnnotationKey = "multivc.vmoperator.vmware.com/sddc"

	// SpecHashAnnotationKey records the hash of the rendered Deployment spec,
	// so an unchanged shard is not updated.
	SpecHashAnnotationKey = "multivc.vmoperator.vmware.com/spec-hash"

	// shardPodName is the "name" label of shard pods. It differs from the
	// global pods' label, so the global pods' anti-affinity and the webhook
	// Service do not select shard pods.
	shardPodName = "vmoperator-shard"

	// VM Operator pods use the host network, so each shard on a node needs
	// its own ports. Shard i binds healthPortBase+i and metricsPortBase+i.
	healthPortBase  = 9460
	metricsPortBase = 9900

	// MaxShards is the number of shards the port ranges allow.
	MaxShards = 100

	managerContainerName = "manager"
	healthPortName       = "health-probe"
	metricsPortName      = "metrics-server"

	// The VSphereServiceAccount whose credentials VM Operator uses, and the
	// labels that the Secret realizing it carries.
	vmopServiceAccountName  = "vmop"
	serviceAccountNameLabel = "vsphere-auth.vmware.com/vsphereserviceaccount"
	serviceAccountSDDCLabel = "vsphere-auth.vmware.com/sddc"
)

// SecretTemplateGVK is the GVK of the secretgen-controller SecretTemplate that
// copies a shard's credentials into a Secret with a fixed name.
var SecretTemplateGVK = schema.GroupVersionKind{
	Group:   "secretgen.carvel.dev",
	Version: "v1alpha1",
	Kind:    "SecretTemplate",
}

// DeploymentName returns the name of the shard Deployment for the SDDC. The
// global Deployment "<prefix>controller-manager" has shards
// "<prefix>shard-<sddc>".
func DeploymentName(globalName, sddcName string) string {
	prefix := strings.TrimSuffix(globalName, "controller-manager")
	if prefix == globalName {
		prefix = globalName + "-"
	}
	return prefix + "shard-" + sddcName
}

// CredentialsSecretName returns the name of the Secret that holds the
// shard's vCenter credentials.
func CredentialsSecretName(base, sddcName string) string {
	return base + "-" + sddcName
}

// LabelValue returns the value of a label that names the SDDC. It is the
// same bounding that the credential Secret's SDDC label uses.
func LabelValue(s string) string {
	s = strings.ToLower(s)
	if len(s) <= validation.LabelValueMaxLength {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// RenderDeployment returns the shard Deployment for the SDDC, derived from
// the global Deployment:
//
//   - one replica, with its own selector and pod labels;
//   - the manager container runs in shard mode for the SDDC, with its own
//     credentials Secret and Deployment name;
//   - no webhook port and no webhook certificate volumes, because shards serve
//     no webhooks;
//   - its own health and metrics ports, and no profiler, because the pods use
//     the host network;
//   - no affinity, because the global pods' anti-affinity would keep a shard
//     off every node that runs a global pod;
//   - a controller owner reference to the global Deployment.
func RenderDeployment(
	global *appsv1.Deployment,
	sddcName string,
	portIndex int,
	config pkgcfg.Config) (*appsv1.Deployment, error) {

	if portIndex < 0 || portIndex >= MaxShards {
		return nil, fmt.Errorf("shard port index %d is outside [0, %d)", portIndex, MaxShards)
	}

	name := DeploymentName(global.Name, sddcName)
	labels := map[string]string{
		ShardLabelKey: LabelValue(sddcName),
		"name":        shardPodName,
	}

	template := *global.Spec.Template.DeepCopy()
	template.Labels = labels
	template.Spec.Affinity = nil

	healthPort := int32(healthPortBase + portIndex)   // bounded by MaxShards
	metricsPort := int32(metricsPortBase + portIndex) // bounded by MaxShards

	webhookMountPath := strings.TrimSuffix(config.WebhookSecretVolumeMountPath, "/")
	mounted := map[string]bool{}
	for i := range template.Spec.Containers {
		c := &template.Spec.Containers[i]
		c.VolumeMounts = withoutWebhookMounts(c.VolumeMounts, webhookMountPath)
		for _, m := range c.VolumeMounts {
			mounted[m.Name] = true
		}
		if c.Name != managerContainerName {
			continue
		}
		c.Args = setArg(c.Args, "--metrics-addr", ":"+strconv.Itoa(int(metricsPort)))
		c.Args = setArg(c.Args, "--health-addr", ":"+strconv.Itoa(int(healthPort)))
		c.Args = setArg(c.Args, "--profiler-address", "0")
		c.Ports = shardPorts(c.Ports, healthPort, metricsPort)
		c.Env = setEnv(c.Env, env.MultiVCMode.String(), string(pkgcfg.MultiVCModeShard))
		c.Env = setEnv(c.Env, env.MultiVCShardSDDC.String(), sddcName)
		c.Env = setEnv(c.Env, env.VCCredsSecretName.String(),
			CredentialsSecretName(config.VCCredsSecretName, sddcName))
		c.Env = setEnv(c.Env, env.DeploymentName.String(), name)
	}
	for i := range template.Spec.InitContainers {
		c := &template.Spec.InitContainers[i]
		c.VolumeMounts = withoutWebhookMounts(c.VolumeMounts, webhookMountPath)
		for _, m := range c.VolumeMounts {
			mounted[m.Name] = true
		}
	}
	volumes := template.Spec.Volumes[:0:0]
	for _, v := range template.Spec.Volumes {
		if mounted[v.Name] {
			volumes = append(volumes, v)
		}
	}
	template.Spec.Volumes = volumes

	d := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: global.Namespace,
			Labels:    map[string]string{ShardLabelKey: LabelValue(sddcName)},
			Annotations: map[string]string{
				PortIndexAnnotationKey: strconv.Itoa(portIndex),
				SDDCAnnotationKey:      sddcName,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "apps/v1",
				Kind:               "Deployment",
				Name:               global.Name,
				UID:                global.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{ShardLabelKey: LabelValue(sddcName)},
			},
			Strategy: appsv1.DeploymentStrategy{
				// A shard has one replica and binds host ports, so the old pod
				// stops before the new one starts.
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Template: template,
		},
	}

	hash, err := specHash(d.Spec)
	if err != nil {
		return nil, err
	}
	d.Annotations[SpecHashAnnotationKey] = hash

	return d, nil
}

// RenderSecretTemplate returns the SecretTemplate that copies the
// credentials of the SDDC's VM Operator service account, from the Secret
// sourceSecretName that realizes it, into the shard's credentials Secret.
func RenderSecretTemplate(
	namespace, sddcName, sourceSecretName string,
	config pkgcfg.Config,
	owner *appsv1.Deployment) *unstructured.Unstructured {

	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(SecretTemplateGVK)
	tmpl.SetName(CredentialsSecretName(config.VCCredsSecretName, sddcName))
	tmpl.SetNamespace(namespace)
	tmpl.SetLabels(map[string]string{ShardLabelKey: LabelValue(sddcName)})
	if owner != nil {
		tmpl.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion:         "apps/v1",
			Kind:               "Deployment",
			Name:               owner.Name,
			UID:                owner.UID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		}})
	}
	tmpl.Object["spec"] = map[string]any{
		"inputResources": []any{
			map[string]any{
				"name": "src",
				"ref": map[string]any{
					"apiVersion": "v1",
					"kind":       "Secret",
					"name":       sourceSecretName,
				},
			},
		},
		"template": map[string]any{
			"type": "Opaque",
			"data": map[string]any{ //nolint:gosec // G101: SecretTemplate expressions, not credentials
				"username": "$(.src.data.username)",
				"password": "$(.src.data.password)",
			},
		},
	}
	return tmpl
}

func withoutWebhookMounts(mounts []corev1.VolumeMount, webhookMountPath string) []corev1.VolumeMount {
	if webhookMountPath == "" {
		return mounts
	}
	out := mounts[:0:0]
	for _, m := range mounts {
		p := strings.TrimSuffix(m.MountPath, "/")
		if p == webhookMountPath || strings.HasPrefix(p, webhookMountPath+"/") {
			continue
		}
		out = append(out, m)
	}
	return out
}

func shardPorts(ports []corev1.ContainerPort, healthPort, metricsPort int32) []corev1.ContainerPort {
	out := make([]corev1.ContainerPort, 0, len(ports))
	for _, p := range ports {
		switch p.Name {
		case healthPortName:
			p.ContainerPort = healthPort
			p.HostPort = 0
		case metricsPortName:
			p.ContainerPort = metricsPort
			p.HostPort = 0
		default:
			// The webhook port and any other port belong to the global
			// process.
			continue
		}
		out = append(out, p)
	}
	return out
}

func setArg(args []string, flag, value string) []string {
	out := make([]string, 0, len(args)+1)
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			continue
		}
		out = append(out, a)
	}
	return append(out, flag+"="+value)
}

func setEnv(vars []corev1.EnvVar, name, value string) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(vars)+1)
	for _, v := range vars {
		if v.Name != name {
			out = append(out, v)
		}
	}
	return append(out, corev1.EnvVar{Name: name, Value: value})
}

func specHash(spec appsv1.DeploymentSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("failed to hash the shard Deployment spec: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}
