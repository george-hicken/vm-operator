// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package virtualmachinesetresourcepolicy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	topologyv1 "github.com/vmware-tanzu/vm-operator/external/tanzu-topology/api/v1alpha1"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	pkglog "github.com/vmware-tanzu/vm-operator/pkg/log"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
	"github.com/vmware-tanzu/vm-operator/pkg/patch"
	"github.com/vmware-tanzu/vm-operator/pkg/providers"
)

const (
	finalizerName           = "vmoperator.vmware.com/virtualmachinesetresourcepolicy"
	deprecatedFinalizerName = "virtualmachinesetresourcepolicy.vmoperator.vmware.com"
)

// ErrShardPolicyDeletionUnsupported means shard cleanup cannot yet prove every
// historical SDDC obligation and must leave the shared deletion gate intact.
var ErrShardPolicyDeletionUnsupported = errors.New("resource policy deletion is unsupported in shard mode")

// AddToManager adds this package's controller to the provided manager.
func AddToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	var (
		controlledType     = &vmopv1.VirtualMachineSetResourcePolicy{}
		controlledTypeName = reflect.TypeOf(controlledType).Elem().Name()

		controllerNameShort = fmt.Sprintf("%s-controller", strings.ToLower(controlledTypeName))
	)

	r := NewReconciler(
		ctx,
		mgr.GetClient(),
		ctrl.Log.WithName("controllers").WithName(controlledTypeName),
		ctx.VMProvider,
	)

	r.APIReader = mgr.GetAPIReader()

	return ctrl.NewControllerManagedBy(mgr).
		For(controlledType).
		Watches(
			&topologyv1.Zone{},
			handler.EnqueueRequestsFromMapFunc(zoneToNamespaceVMSRP(mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: ctx.GetMaxConcurrentReconciles(controllerNameShort, ctx.MaxConcurrentReconciles),
			LogConstructor:          pkglog.ControllerLogConstructor(controllerNameShort, controlledType, mgr.GetScheme()),
		}).
		Complete(r)
}

func NewReconciler(
	ctx context.Context,
	client client.Client,
	logger logr.Logger,
	vmProvider providers.VirtualMachineProviderInterface) *Reconciler {
	return &Reconciler{
		Context:    ctx,
		Client:     client,
		APIReader:  client,
		Logger:     logger,
		VMProvider: vmProvider,
	}
}

func zoneToNamespaceVMSRP(
	c client.Client) func(context.Context, client.Object) []reconcile.Request {

	return func(ctx context.Context, o client.Object) []reconcile.Request {
		zone := o.(*topologyv1.Zone)

		list := vmopv1.VirtualMachineSetResourcePolicyList{}
		if err := c.List(ctx, &list, client.InNamespace(zone.Namespace)); err != nil {
			return nil
		}

		var reconcileRequests []reconcile.Request
		for i := range list.Items {
			reconcileRequests = append(reconcileRequests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&list.Items[i]),
			})
		}
		return reconcileRequests
	}
}

// Reconciler reconciles a VirtualMachineSetResourcePolicy object.
type Reconciler struct {
	// APIReader bypasses the cache when retrying a shared status write.
	APIReader client.Reader
	Context   context.Context
	client.Client
	Logger     logr.Logger
	VMProvider providers.VirtualMachineProviderInterface
}

// ReconcileNormal reconciles a VirtualMachineSetResourcePolicy.
func (r *Reconciler) ReconcileNormal(ctx *pkgctx.VirtualMachineSetResourcePolicyContext) error {
	if pkgcfg.FromContext(ctx).MultiVC.IsShard() {
		// Reconcile registers the gate first. Check the live object as well,
		// since this method is also callable directly by tests and callers.
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		live := &vmopv1.VirtualMachineSetResourcePolicy{}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(ctx.ResourcePolicy), live); err != nil {
			return err
		}
		if live.UID != ctx.ResourcePolicy.UID {
			return fmt.Errorf("resource policy was replaced before shard provider reconciliation")
		}
		if !live.DeletionTimestamp.IsZero() {
			return r.ReconcileDelete(ctx)
		}
		if !controllerutil.ContainsFinalizer(live, finalizerName) {
			return fmt.Errorf("resource policy %s has no persisted deletion gate", client.ObjectKeyFromObject(live))
		}
		return r.VMProvider.CreateOrUpdateVirtualMachineSetResourcePolicy(ctx, ctx.ResourcePolicy)
	}
	if !controllerutil.ContainsFinalizer(ctx.ResourcePolicy, finalizerName) {
		// If the object has the deprecated finalizer, remove it.
		if updated := controllerutil.RemoveFinalizer(ctx.ResourcePolicy, deprecatedFinalizerName); updated {
			ctx.Logger.V(5).Info("Removed deprecated finalizer", "finalizerName", deprecatedFinalizerName)
		}

		// Return here so the VirtualMachineSetResourcePolicy can be patched immediately. This ensures that
		// the resource policies are cleaned up properly when they are deleted.
		controllerutil.AddFinalizer(ctx.ResourcePolicy, finalizerName)
		return nil
	}

	ctx.Logger.Info("Reconciling VirtualMachineSetResourcePolicy")
	defer func() {
		ctx.Logger.Info("Finished Reconciling VirtualMachineSetResourcePolicy")
	}()

	if err := r.VMProvider.CreateOrUpdateVirtualMachineSetResourcePolicy(ctx, ctx.ResourcePolicy); err != nil {
		ctx.Logger.Error(err, "Provider failed to reconcile VirtualMachineSetResourcePolicy")
		return err
	}

	return nil
}

// deleteResourcePolicy deletes a VirtualMachineSetResourcePolicy resource.
func (r *Reconciler) deleteResourcePolicy(ctx *pkgctx.VirtualMachineSetResourcePolicyContext) error {
	resourcePolicy := ctx.ResourcePolicy

	// Skip deleting a VirtualMachineSetResourcePolicy if it is referenced by a VirtualMachine.
	vmsInNamespace := &vmopv1.VirtualMachineList{}
	if err := r.List(ctx, vmsInNamespace, client.InNamespace(resourcePolicy.Namespace)); err != nil {
		ctx.Logger.Error(err, "Failed to list VMs in namespace", "namespace", resourcePolicy.Namespace)
		return err
	}

	for _, vm := range vmsInNamespace.Items {
		if reserved := vm.Spec.Reserved; reserved != nil && reserved.ResourcePolicyName == resourcePolicy.Name {
			return fmt.Errorf("failing VirtualMachineSetResourcePolicy deletion since VM: '%s' is referencing it, resourcePolicyName: '%s'",
				vm.NamespacedName(), resourcePolicy.NamespacedName())
		}
	}

	ctx.Logger.V(4).Info("Attempting to delete VirtualMachineSetResourcePolicy")
	if err := r.VMProvider.DeleteVirtualMachineSetResourcePolicy(ctx, resourcePolicy); err != nil {
		ctx.Logger.Error(err, "error in deleting VirtualMachineSetResourcePolicy")
		return err
	}
	ctx.Logger.Info("Deleted VirtualMachineSetResourcePolicy successfully")

	return nil
}

func (r *Reconciler) ReconcileDelete(ctx *pkgctx.VirtualMachineSetResourcePolicyContext) error {
	if pkgcfg.FromContext(ctx).MultiVC.IsShard() {
		// The shared gate covers historical resources in every SDDC. A
		// shard cannot prove their full cleanup from current topology or
		// status, so automatic shard deletion remains unsupported.
		return fmt.Errorf("%w for %s uid %s: historical SDDC cleanup obligations need review",
			ErrShardPolicyDeletionUnsupported,
			client.ObjectKeyFromObject(ctx.ResourcePolicy), ctx.ResourcePolicy.UID)
	}
	ctx.Logger.Info("Reconciling VirtualMachineSetResourcePolicy Deletion")
	defer func() {
		ctx.Logger.Info("Finished Reconciling VirtualMachineSetResourcePolicy Deletion")
	}()

	if controllerutil.ContainsFinalizer(ctx.ResourcePolicy, finalizerName) ||
		controllerutil.ContainsFinalizer(ctx.ResourcePolicy, deprecatedFinalizerName) {
		if err := r.deleteResourcePolicy(ctx); err != nil {
			return err
		}

		controllerutil.RemoveFinalizer(ctx.ResourcePolicy, finalizerName)
		controllerutil.RemoveFinalizer(ctx.ResourcePolicy, deprecatedFinalizerName)
	}

	return nil
}

// ensureSharedFinalizer persists the deletion gate before a shard creates
// resources in vCenter. It uses a fresh API read and an optimistic patch so
// concurrent metadata writers cannot lose each other's finalizers.
func (r *Reconciler) ensureSharedFinalizer(
	ctx context.Context,
	observed *vmopv1.VirtualMachineSetResourcePolicy) (*vmopv1.VirtualMachineSetResourcePolicy, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	key := client.ObjectKeyFromObject(observed)
	var latest *vmopv1.VirtualMachineSetResourcePolicy
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest = &vmopv1.VirtualMachineSetResourcePolicy{}
		if err := reader.Get(ctx, key, latest); err != nil {
			return err
		}
		if latest.UID != observed.UID {
			return fmt.Errorf("resource policy %s was replaced while adding its deletion gate", key)
		}
		if !latest.DeletionTimestamp.IsZero() || controllerutil.ContainsFinalizer(latest, finalizerName) {
			return nil
		}
		base := latest.DeepCopy()
		controllerutil.AddFinalizer(latest, finalizerName)
		return r.Client.Patch(ctx, latest, client.MergeFromWithOptions(
			base, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return nil, fmt.Errorf("failed to persist resource policy deletion gate: %w", err)
	}
	return latest, nil
}

// +kubebuilder:rbac:groups=vmoperator.vmware.com,resources=virtualmachinesetresourcepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vmoperator.vmware.com,resources=virtualmachinesetresourcepolicies/status,verbs=get;update;patch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	ctx = pkgcfg.JoinContext(ctx, r.Context)
	shardMode := pkgcfg.FromContext(ctx).MultiVC.IsShard()

	rp := &vmopv1.VirtualMachineSetResourcePolicy{}
	if err := r.Get(ctx, req.NamespacedName, rp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if shardMode && rp.DeletionTimestamp.IsZero() {
		// Persist the gate and then observe deletion again on the fresh object.
		// The provider must never run on a pre-registration snapshot.
		var err error
		rp, err = r.ensureSharedFinalizer(ctx, rp)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	rpCtx := &pkgctx.VirtualMachineSetResourcePolicyContext{
		Context:        ctx,
		Logger:         pkglog.FromContextOrDefault(ctx),
		ResourcePolicy: rp,
	}
	if shardMode && !rp.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.ReconcileDelete(rpCtx)
	}

	before := rp.DeepCopy()
	patchHelper, err := patch.NewHelper(rp, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to init patch helper for %s: %w", rpCtx, err)
	}
	defer func() {
		patchObject := rp
		if shardMode {
			reader := r.APIReader
			if reader == nil {
				reader = r.Client
			}
			if err := multivc.PatchResourcePolicyStatus(ctx, r.Client, reader, before, rp); err != nil {
				if reterr == nil {
					reterr = err
				}
				rpCtx.Logger.Error(err, "shard status patch failed")
				// Leave the shared deletion gate intact on persistence failure.
				return
			}
			// The lists have already been merged under optimistic locking.
			// Leave only metadata/spec changes for the ordinary patch helper.
			patchObject = rp.DeepCopy()
			patchObject.Status = before.Status
			// The optimistic gate patch owns the finalizer list. A stale
			// deferred patch must not restore or drop a concurrent gate.
			patchObject.Finalizers = before.Finalizers
		}
		if err := patchHelper.Patch(ctx, patchObject); err != nil {
			if reterr == nil {
				reterr = err
			}
			rpCtx.Logger.Error(err, "patch failed")
		}
	}()

	if !rp.ObjectMeta.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.ReconcileDelete(rpCtx)
	}

	return ctrl.Result{}, r.ReconcileNormal(rpCtx)
}
