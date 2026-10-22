// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

// Package vmidentity carries the identity that VM Operator stamps on the VC
// VMs it creates, and checks before it adopts one: the Supervisor that manages
// the VM and the organization of the VM's namespace.
package vmidentity

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	// ExtraConfigSupervisorID is the ExtraConfig key of the ID of the
	// Supervisor that manages the VM.
	ExtraConfigSupervisorID = "vmservice.supervisor.id"

	// ExtraConfigNamespaceOrgID is the ExtraConfig key of the organization ID
	// of the VM's namespace. It is set only when the namespace has one.
	ExtraConfigNamespaceOrgID = "vmservice.namespace.orgID"

	// NamespaceOrgIDLabel is the namespace label that holds the organization
	// ID.
	NamespaceOrgIDLabel = "vmware-system-vcf/organization-id"

	// wcpClusterConfigNamespace, wcpClusterConfigName and wcpClusterConfigKey
	// locate the Supervisor's cluster config, whose supervisor_id field is the
	// Supervisor ID.
	wcpClusterConfigNamespace = "kube-system"
	wcpClusterConfigName      = "wcp-cluster-config"
	wcpClusterConfigKey       = "wcp-cluster-config.yaml"
)

// Identity is what VM Operator stamps on a VC VM.
type Identity struct {
	// SupervisorID is the ID of this Supervisor. It is empty when the
	// Supervisor's cluster config has none.
	SupervisorID string

	// OrgID is the organization ID of the VM's namespace, or empty.
	OrgID string
}

type contextKey struct{}
type adoptionKey struct{}

// WithContext returns a context that carries the identity.
func WithContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the identity the context carries, or the zero value.
func FromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(contextKey{}).(Identity)
	return id
}

// WithAdoption permits the create/update path to adopt an explicitly imported
// or restored VM whose legacy VC config has no Supervisor stamp.
func WithAdoption(ctx context.Context) context.Context {
	return context.WithValue(ctx, adoptionKey{}, true)
}

// AdoptionRequested reports whether the caller is the create/update adoption
// operation. Ordinary lookup, including deletion, never sets this intent.
func AdoptionRequested(ctx context.Context) bool {
	allowed, _ := ctx.Value(adoptionKey{}).(bool)
	return allowed
}

// GetSupervisorID returns the supervisor_id of the Supervisor's
// wcp-cluster-config ConfigMap, or "" when the ConfigMap or the field does
// not exist.
func GetSupervisorID(ctx context.Context, c ctrlclient.Reader) (string, error) {
	var cm corev1.ConfigMap
	key := ctrlclient.ObjectKey{Namespace: wcpClusterConfigNamespace, Name: wcpClusterConfigName}
	if err := c.Get(ctx, key, &cm); err != nil {
		if ctrlclient.IgnoreNotFound(err) == nil {
			return "", nil
		}
		return "", fmt.Errorf("failed to get %s: %w", key, err)
	}
	var cfg struct {
		SupervisorID string `json:"supervisor_id"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data[wcpClusterConfigKey]), &cfg); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", key, err)
	}
	return cfg.SupervisorID, nil
}

// GetNamespaceOrgID returns the organization ID label of the namespace, or ""
// when it has none.
func GetNamespaceOrgID(ctx context.Context, c ctrlclient.Reader, namespace string) (string, error) {
	var ns corev1.Namespace
	if err := c.Get(ctx, ctrlclient.ObjectKey{Name: namespace}, &ns); err != nil {
		return "", fmt.Errorf("failed to get namespace %s: %w", namespace, err)
	}
	return ns.Labels[NamespaceOrgIDLabel], nil
}

// SupervisorMatches reports whether a stamped VC VM belongs to this
// Supervisor. Unstamped legacy VMs need a separate adoption decision.
func SupervisorMatches(stamped, ours string) bool {
	return stamped != "" && ours != "" && stamped == ours
}

// ExtraConfig returns the ExtraConfig stamps for the identity. A stamp whose
// value is empty is omitted, so that it is neither written nor cleared.
func (id Identity) ExtraConfig() map[string]string {
	ec := map[string]string{}
	if id.SupervisorID != "" {
		ec[ExtraConfigSupervisorID] = id.SupervisorID
	}
	if id.OrgID != "" {
		ec[ExtraConfigNamespaceOrgID] = id.OrgID
	}
	return ec
}
