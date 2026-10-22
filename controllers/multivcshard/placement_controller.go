// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivcshard

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcond "github.com/vmware-tanzu/vm-operator/pkg/conditions"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	"github.com/vmware-tanzu/vm-operator/pkg/patch"
	"github.com/vmware-tanzu/vm-operator/pkg/topology"
)

const (
	placementControllerName = "multivc-placement-controller"
	placementRetryInterval  = 30 * time.Second
)

// AddPlacementToManager adds the controller that assigns a zone to a VM that
// has none while the Supervisor has several SDDCs. It runs in the
// multi-vCenter global mode only.
func AddPlacementToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	logger := ctrl.Log.WithName("controllers").WithName("MultiVCPlacement")
	r := &PlacementReconciler{
		Client:   mgr.GetClient(),
		Context:  ctx,
		Logger:   logger,
		Verifier: multivc.NewAllowAllVerifier(logger),
		Choose:   rand.IntN,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(placementControllerName).
		For(&vmopv1.VirtualMachine{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			SkipNameValidation:      SkipNameValidation,
		}).
		Complete(r)
}

// PlacementReconciler gives every VM a zone once the Supervisor has several
// SDDCs.
//
// A shard owns the VMs in its SDDC's zones, so a VM without a zone belongs to
// no shard. While the Supervisor has one SDDC, its shard places such a VM in a
// zone, as a single-vCenter VM Operator does. Once it has several, this
// controller writes the zone label of a VM that has none, choosing uniformly
// at random among the zones of entitled SDDCs that the VM's namespace has.
// That is the only write it makes to a VM with a zone to choose from. When
// there is none, it sets the Placed condition false with reason
// NoEligibleZone, and removes that condition again once the VM has a zone.
//
// Random choice is the prototype's placement. Placement from data the shards
// surface, such as capacity, images and storage, is spec 51 D-51-10.
type PlacementReconciler struct {
	ctrlclient.Client
	Context  context.Context
	Logger   logr.Logger
	Verifier multivc.EntitlementVerifier

	// Choose returns a number in [0, n).
	Choose func(n int) int
}

// Reconcile assigns the zone of the VM named by req, when it has none.
func (r *PlacementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = pkgcfg.JoinContext(ctx, r.Context)

	vm := &vmopv1.VirtualMachine{}
	if err := r.Get(ctx, req.NamespacedName, vm); err != nil {
		return ctrl.Result{}, ctrlclient.IgnoreNotFound(err)
	}
	if !vm.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if vm.Labels[corev1.LabelTopologyZone] != "" {
		return ctrl.Result{}, r.clearNoEligibleZone(ctx, vm)
	}

	sddcs, err := multivc.ListSDDCs(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(sddcs) <= 1 {
		// The sole shard places the VM.
		return ctrl.Result{}, nil
	}

	zones, err := r.eligibleZones(ctx, vm.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(zones) == 0 {
		// Eligibility can recover without a VM event; dependencies are not watched.
		return ctrl.Result{RequeueAfter: placementRetryInterval}, r.markNoEligibleZone(ctx, vm, len(sddcs))
	}

	zone := zones[r.Choose(len(zones))]

	// Preserve a zone assigned after our cached read. A conflict retries the
	// reconcile, which then leaves the winning assignment unchanged.
	orig := vm.DeepCopy()
	if vm.Labels == nil {
		vm.Labels = map[string]string{}
	}
	vm.Labels[corev1.LabelTopologyZone] = zone
	if err := r.Patch(ctx, vm, ctrlclient.MergeFromWithOptions(orig, ctrlclient.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set the zone of VM %s: %w", req.NamespacedName, err)
	}
	r.Logger.Info("Assigned VM to a zone", "vm", req.NamespacedName, "zone", zone, "candidates", zones)
	return ctrl.Result{}, nil
}

// eligibleZones returns the zones the VM's namespace has whose
// AvailabilityZone is owned by an entitled SDDC, in order.
func (r *PlacementReconciler) eligibleZones(ctx context.Context, namespace string) ([]string, error) {
	sddcOK := map[string]bool{}
	entitled := func(name string) (bool, error) {
		if ok, seen := sddcOK[name]; seen {
			return ok, nil
		}
		obj := multivc.NewSDDC()
		if err := r.Get(ctx, ctrlclient.ObjectKey{Name: name}, obj); err != nil {
			if ctrlclient.IgnoreNotFound(err) == nil {
				sddcOK[name] = false
				return false, nil
			}
			return false, err
		}
		ok := r.Verifier.Verify(ctx, obj) == nil
		sddcOK[name] = ok
		return ok, nil
	}

	var names []string
	if pkgcfg.FromContext(ctx).Features.WorkloadDomainIsolation {
		zones, err := topology.GetZones(ctx, r.Client, namespace)
		if err != nil && !errors.Is(err, topology.ErrNoZones) {
			return nil, err
		}
		for _, z := range zones {
			if z.DeletionTimestamp.IsZero() {
				names = append(names, z.Name)
			}
		}
	} else {
		azs, err := topology.GetAvailabilityZones(ctx, r.Client)
		if err != nil && !errors.Is(err, topology.ErrNoAvailabilityZones) {
			return nil, err
		}
		for _, az := range azs {
			if _, ok := az.Spec.Namespaces[namespace]; ok {
				names = append(names, az.Name)
			}
		}
	}

	var eligible []string
	for _, name := range names {
		az, err := topology.GetAvailabilityZone(ctx, r.Client, name)
		if err != nil {
			if ctrlclient.IgnoreNotFound(err) == nil {
				continue
			}
			return nil, err
		}
		owner := multivc.SDDCOfAvailabilityZone(az)
		if owner == "" {
			continue
		}
		ok, err := entitled(owner)
		if err != nil {
			return nil, err
		}
		if ok {
			eligible = append(eligible, name)
		}
	}
	slices.Sort(eligible)
	return eligible, nil
}

func (r *PlacementReconciler) markNoEligibleZone(ctx context.Context, vm *vmopv1.VirtualMachine, sddcCount int) error {
	if c := pkgcond.Get(vm, multivc.ConditionPlaced); c != nil && c.Reason == multivc.ReasonNoEligibleZone {
		return nil
	}
	helper, err := patch.NewHelper(vm, r.Client)
	if err != nil {
		return err
	}
	pkgcond.MarkFalse(vm, multivc.ConditionPlaced, multivc.ReasonNoEligibleZone,
		"The Supervisor spans %d SDDCs, and namespace %s has no zone of an entitled SDDC to place the VM in",
		sddcCount, vm.Namespace)
	return helper.Patch(ctx, vm)
}

func (r *PlacementReconciler) clearNoEligibleZone(ctx context.Context, vm *vmopv1.VirtualMachine) error {
	if c := pkgcond.Get(vm, multivc.ConditionPlaced); c == nil || c.Reason != multivc.ReasonNoEligibleZone {
		return nil
	}
	helper, err := patch.NewHelper(vm, r.Client)
	if err != nil {
		return err
	}
	pkgcond.Delete(vm, multivc.ConditionPlaced)
	return helper.Patch(ctx, vm)
}
