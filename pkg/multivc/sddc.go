// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SDDCGroup is the API group of the SDDC resource.
	SDDCGroup = "iaas.vmware.com"

	// SDDCKind is the kind of the SDDC resource.
	SDDCKind = "SDDC"

	// AnnotationVCenterUUID records the instance UUID of the SDDC's vCenter.
	AnnotationVCenterUUID = "vmware-system/vcenter-uuid"

	// AnnotationDatacenter records the MoID of the SDDC's datacenter.
	AnnotationDatacenter = "vmware-system/datacenter"

	defaultVCPort = "443"
)

// SDDCGroupVersionKind is the GVK of the SDDC resource. VM Operator does not
// import the SDDC API types, so SDDCs are read as unstructured objects.
var SDDCGroupVersionKind = schema.GroupVersionKind{
	Group:   SDDCGroup,
	Version: "v1alpha1",
	Kind:    SDDCKind,
}

// ErrSDDCNotFound is returned when the named SDDC does not exist.
var ErrSDDCNotFound = errors.New("sddc not found")

// SDDC is the part of an SDDC resource that VM Operator uses.
type SDDC struct {
	Name              string
	CreationTimestamp metav1.Time
	EndpointURL       string
	TrustBundle       string
	VCenterUUID       string
	Datacenter        string
}

// SDDCFromUnstructured returns the SDDC that obj describes.
func SDDCFromUnstructured(obj *unstructured.Unstructured) SDDC {
	endpoint, _, _ := unstructured.NestedString(obj.Object, "spec", "endpointURL")
	trust, _, _ := unstructured.NestedString(obj.Object, "spec", "trustBundle")
	annotations := obj.GetAnnotations()
	return SDDC{
		Name:              obj.GetName(),
		CreationTimestamp: obj.GetCreationTimestamp(),
		EndpointURL:       endpoint,
		TrustBundle:       trust,
		VCenterUUID:       annotations[AnnotationVCenterUUID],
		Datacenter:        annotations[AnnotationDatacenter],
	}
}

// NewSDDCList returns an empty unstructured list of SDDC resources.
func NewSDDCList() *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(SDDCGroupVersionKind.GroupVersion().WithKind(SDDCKind + "List"))
	return list
}

// NewSDDC returns an empty unstructured SDDC resource.
func NewSDDC() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(SDDCGroupVersionKind)
	return obj
}

// ListSDDCs returns the SDDCs of the Supervisor, ordered by name.
func ListSDDCs(ctx context.Context, reader ctrlclient.Reader) ([]SDDC, error) {
	list := NewSDDCList()
	if err := reader.List(ctx, list); err != nil {
		return nil, fmt.Errorf("failed to list SDDCs: %w", err)
	}
	sddcs := make([]SDDC, 0, len(list.Items))
	for i := range list.Items {
		sddcs = append(sddcs, SDDCFromUnstructured(&list.Items[i]))
	}
	slices.SortFunc(sddcs, func(a, b SDDC) int { return strings.Compare(a.Name, b.Name) })
	return sddcs, nil
}

// FindSDDC returns the named SDDC from sddcs.
func FindSDDC(sddcs []SDDC, name string) (SDDC, error) {
	for _, s := range sddcs {
		if s.Name == name {
			return s, nil
		}
	}
	return SDDC{}, fmt.Errorf("%w: %q", ErrSDDCNotFound, name)
}

// PrimarySDDC returns the name of the primary SDDC: the oldest SDDC by
// creation time, with ties broken by name. The SDDC created when the
// Supervisor is enabled is older than any SDDC a later join adds, so it is the
// primary. The primary shard owns the zones that no SDDC owns and the VMs that
// have no zone.
func PrimarySDDC(sddcs []SDDC) string {
	if len(sddcs) == 0 {
		return ""
	}
	primary := sddcs[0]
	for _, s := range sddcs[1:] {
		switch {
		case s.CreationTimestamp.Before(&primary.CreationTimestamp):
			primary = s
		case s.CreationTimestamp.Equal(&primary.CreationTimestamp) && s.Name < primary.Name:
			primary = s
		}
	}
	return primary.Name
}

// Endpoint returns the vCenter host and port from the SDDC's endpoint URL. A
// URL without a port uses 443.
func (s SDDC) Endpoint() (string, string, error) {
	u, err := url.Parse(s.EndpointURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse endpoint %q of SDDC %q: %w", s.EndpointURL, s.Name, err)
	}
	if u.Scheme != "https" || u.Hostname() == "" {
		return "", "", fmt.Errorf("endpoint %q of SDDC %q is not an https URL with a host", s.EndpointURL, s.Name)
	}
	port := u.Port()
	if port == "" {
		port = defaultVCPort
	}
	return u.Hostname(), port, nil
}

// HostPort returns the endpoint as host:port.
func (s SDDC) HostPort() (string, error) {
	host, port, err := s.Endpoint()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}
