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

// noZone is the value a shard with no zones selects, so that its
// selector matches no VM. An "in" requirement needs at least one value, and
// this one is reserved for the purpose.
const noZone = "multivc.vmoperator.vmware.com-no-zone"

// VMSelector returns the label selector that limits a shard's VM cache to the
// VMs it is responsible for:
//
//   - a sole shard, whose SDDC is the only SDDC, selects every VM, as a
//     single-vCenter VM Operator does;
//   - otherwise a shard selects the VMs whose zone label is one of its owned
//     zones. A VM without a zone label is selected by no shard: the global
//     process reports that it needs a zone.
//
// Outside shard mode it returns labels.Everything.
func VMSelector(m pkgcfg.MultiVC) (labels.Selector, error) {
	if !m.IsShard() || m.Sole {
		return labels.Everything(), nil
	}

	values := pkgcfg.StringToSlice(m.OwnedZones)
	if len(values) == 0 {
		values = []string{noZone}
	}

	req, err := labels.NewRequirement(corev1.LabelTopologyZone, selection.In, values)
	if err != nil {
		return nil, fmt.Errorf("failed to build the zone selector: %w", err)
	}
	return labels.NewSelector().Add(*req), nil
}

const (
	// ConditionPlaced is the VM condition type the global process sets on a
	// VM it cannot place in a zone.
	ConditionPlaced = "Placed"

	// ReasonNoEligibleZone is the reason of a false ConditionPlaced: the
	// Supervisor spans several SDDCs, the VM has no zone, and no zone of an
	// entitled SDDC is available to the VM's namespace.
	ReasonNoEligibleZone = "NoEligibleZone"
)
