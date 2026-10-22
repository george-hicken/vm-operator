// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// noZone is the value a non-primary shard with no zones selects, so that its
// selector matches no VM. An "in" requirement needs at least one value, and
// this one is reserved for the purpose.
const noZone = "multivc.vmoperator.vmware.com-no-zone"

// VMSelector returns the label selector that limits a shard's VM cache to the
// VMs it is responsible for:
//
//   - a non-primary shard selects the VMs whose zone label is one of its
//     owned zones;
//   - the primary shard selects every VM whose zone label is not one of the
//     foreign zones. Because "notin" also matches a VM without the label,
//     that includes VMs with no zone and VMs in unowned zones.
//
// Outside shard mode it returns labels.Everything.
func VMSelector(m pkgcfg.MultiVC) (labels.Selector, error) {
	if !m.IsShard() {
		return labels.Everything(), nil
	}

	var (
		op     = selection.In
		values = pkgcfg.StringToSlice(m.OwnedZones)
	)
	if m.Primary {
		op = selection.NotIn
		values = pkgcfg.StringToSlice(m.ForeignZones)
		if len(values) == 0 {
			return labels.Everything(), nil
		}
	} else if len(values) == 0 {
		values = []string{noZone}
	}

	req, err := labels.NewRequirement(corev1.LabelTopologyZone, op, values)
	if err != nil {
		return nil, fmt.Errorf("failed to build the zone selector: %w", err)
	}
	return labels.NewSelector().Add(*req), nil
}
