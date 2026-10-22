// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	topologyv1 "github.com/vmware-tanzu/vm-operator/external/tanzu-topology/api/v1alpha1"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// SDDCOfAvailabilityZone returns the name of the SDDC that owns the
// AvailabilityZone, or "" when no SDDC owns it. The owner is found through the
// AvailabilityZone's owner references, which the creator of the
// AvailabilityZone writes.
func SDDCOfAvailabilityZone(az topologyv1.AvailabilityZone) string {
	return sddcOwner(az.OwnerReferences)
}

func sddcOwner(refs []metav1.OwnerReference) string {
	for _, ref := range refs {
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			continue
		}
		if gv.Group == SDDCGroup && ref.Kind == SDDCKind {
			return ref.Name
		}
	}
	return ""
}

// Topology is a shard's view of the Supervisor's SDDCs and zones.
type Topology struct {
	// SDDCs are the names of all SDDCs of the Supervisor, in order.
	SDDCs []string

	// SDDC is the shard's SDDC.
	SDDC SDDC

	// Owned are the zones whose AvailabilityZone the shard's SDDC owns.
	Owned []string

	// Foreign are the zones whose AvailabilityZone another SDDC owns.
	Foreign []string

	// Unowned are the zones whose AvailabilityZone no SDDC owns. A sole
	// shard is responsible for them. When several SDDCs exist no shard is,
	// because the zone cannot be resolved to a vCenter.
	Unowned []string
}

// ComputeTopology returns the topology of the shard for shardSDDC.
func ComputeTopology(
	sddcs []SDDC,
	azs []topologyv1.AvailabilityZone,
	shardSDDC string) (Topology, error) {

	self, err := FindSDDC(sddcs, shardSDDC)
	if err != nil {
		return Topology{}, err
	}

	t := Topology{SDDC: self}
	for _, s := range sddcs {
		t.SDDCs = append(t.SDDCs, s.Name)
	}
	for _, az := range azs {
		switch owner := SDDCOfAvailabilityZone(az); owner {
		case shardSDDC:
			t.Owned = append(t.Owned, az.Name)
		case "":
			t.Unowned = append(t.Unowned, az.Name)
		default:
			t.Foreign = append(t.Foreign, az.Name)
		}
	}
	slices.Sort(t.Owned)
	slices.Sort(t.Foreign)
	slices.Sort(t.Unowned)
	return t, nil
}

// LoadTopology reads the SDDCs and AvailabilityZones and returns the topology
// of the shard for shardSDDC.
func LoadTopology(
	ctx context.Context,
	reader ctrlclient.Reader,
	shardSDDC string) (Topology, error) {

	sddcs, err := ListSDDCs(ctx, reader)
	if err != nil {
		return Topology{}, err
	}
	var azList topologyv1.AvailabilityZoneList
	if err := reader.List(ctx, &azList); err != nil {
		return Topology{}, fmt.Errorf("failed to list availability zones: %w", err)
	}
	return ComputeTopology(sddcs, azList.Items, shardSDDC)
}

// IsSole returns true when the shard's SDDC is the only SDDC.
func (t Topology) IsSole() bool {
	return len(t.SDDCs) == 1
}

// Apply records the topology in the multi-vCenter configuration.
func (t Topology) Apply(m *pkgcfg.MultiVC) {
	m.Sole = t.IsSole()
	m.OwnedZones = pkgcfg.SliceToString(t.Owned)
}

// Fingerprint returns a string that changes whenever the shard's
// responsibilities or its vCenter endpoint change. A shard compares it with
// the fingerprint it started with, and restarts when they differ.
func (t Topology) Fingerprint() string {
	return strings.Join([]string{
		"sddcs=" + strings.Join(t.SDDCs, ","),
		"owned=" + strings.Join(t.Owned, ","),
		"foreign=" + strings.Join(t.Foreign, ","),
		"unowned=" + strings.Join(t.Unowned, ","),
		"endpoint=" + t.SDDC.EndpointURL,
		"vcenter=" + t.SDDC.VCenterUUID,
		"datacenter=" + t.SDDC.Datacenter,
		"trust=" + t.SDDC.TrustBundle,
	}, "\n")
}
