// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivcshard

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/multivc"
)

// +kubebuilder:rbac:groups=iaas.vmware.com,resources=sddcs,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=secretgen.carvel.dev,resources=secrettemplates,verbs=get;list;watch;create;update;patch;delete

const controllerName = "multivc-shard-controller"

// credentialsRequeue is how long to wait for the Secret that realizes an
// SDDC's VM Operator service account, which supervisor-agent writes.
const credentialsRequeue = 15 * time.Second

// SkipNameValidation is used for testing to allow multiple controllers with the
// same name.
var SkipNameValidation *bool

// AddToManager adds the controller that renders one VM Operator shard per
// SDDC. It runs in the multi-vCenter global mode only.
func AddToManager(ctx *pkgctx.ControllerManagerContext, mgr manager.Manager) error {
	logger := ctrl.Log.WithName("controllers").WithName("MultiVCShard")
	r := NewReconciler(ctx, mgr.GetClient(), logger, multivc.NewAllowAllVerifier(logger))

	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		For(multivc.NewSDDC()).
		Watches(&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.sddcsForDeployment),
			builder.WithPredicates(predicate.NewPredicateFuncs(r.isShardOrGlobal))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			SkipNameValidation:      SkipNameValidation,
		}).
		Complete(r)
}

// NewReconciler returns a new Reconciler.
func NewReconciler(
	ctx context.Context,
	client ctrlclient.Client,
	logger logr.Logger,
	verifier multivc.EntitlementVerifier) *Reconciler {

	return &Reconciler{
		Context:  ctx,
		Client:   client,
		Logger:   logger,
		Verifier: verifier,
	}
}

// Reconciler renders the shard Deployment and credential SecretTemplate of an
// SDDC, and deletes them when the SDDC is gone or is not entitled.
//
// It renders a shard for every SDDC, the first included, so in split mode the
// shards use only the per-SDDC credentials it renders. The installer's
// SecretTemplate for the first SDDC serves the combined mode.
type Reconciler struct {
	ctrlclient.Client
	Context  context.Context
	Logger   logr.Logger
	Verifier multivc.EntitlementVerifier
}

// Reconcile renders the shard of the SDDC named by req.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = pkgcfg.JoinContext(ctx, r.Context)
	config := pkgcfg.FromContext(ctx)
	logger := r.Logger.WithValues("sddc", req.Name)

	sddc := multivc.NewSDDC()
	if err := r.Get(ctx, ctrlclient.ObjectKey{Name: req.Name}, sddc); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.deleteShard(ctx, logger, config, req.Name)
	}
	if !sddc.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.deleteShard(ctx, logger, config, req.Name)
	}
	if err := r.Verifier.Verify(ctx, sddc); err != nil {
		logger.Error(err, "SDDC is not entitled to a shard")
		return ctrl.Result{}, r.deleteShard(ctx, logger, config, req.Name)
	}

	global := &appsv1.Deployment{}
	globalKey := ctrlclient.ObjectKey{Namespace: config.PodNamespace, Name: config.DeploymentName}
	if err := r.Get(ctx, globalKey, global); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get the global Deployment %s: %w", globalKey, err)
	}

	shard, err := r.ensureDeployment(ctx, logger, config, global, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	ready, err := r.ensureSecretTemplate(ctx, logger, config, shard, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: credentialsRequeue}, nil
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) ensureDeployment(
	ctx context.Context,
	logger logr.Logger,
	config pkgcfg.Config,
	global *appsv1.Deployment,
	sddcName string) (*appsv1.Deployment, error) {

	name := DeploymentName(global.Name, sddcName)

	var shards appsv1.DeploymentList
	if err := r.List(ctx, &shards,
		ctrlclient.InNamespace(global.Namespace),
		ctrlclient.HasLabels{ShardLabelKey}); err != nil {
		return nil, fmt.Errorf("failed to list shard Deployments: %w", err)
	}

	// A shard keeps its port index. A new shard takes the lowest index no
	// other shard uses.
	var (
		existing  *appsv1.Deployment
		portIndex = -1
		used      = map[int]bool{}
	)
	for i := range shards.Items {
		d := &shards.Items[i]
		idx, err := strconv.Atoi(d.Annotations[PortIndexAnnotationKey])
		switch {
		case d.Name == name:
			existing = d
			if err == nil {
				portIndex = idx
			}
		case err == nil:
			used[idx] = true
		}
	}
	if portIndex < 0 || used[portIndex] {
		portIndex = -1
		for i := range MaxShards {
			if !used[i] {
				portIndex = i
				break
			}
		}
	}
	if portIndex < 0 {
		return nil, fmt.Errorf("no free shard port index for SDDC %q", sddcName)
	}

	desired, err := RenderDeployment(global, sddcName, portIndex, config)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		if err := r.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("failed to create shard Deployment %s: %w", name, err)
		}
		logger.Info("Created shard Deployment", "deployment", name, "portIndex", portIndex)
		return desired, nil
	}

	if existing.Annotations[SpecHashAnnotationKey] == desired.Annotations[SpecHashAnnotationKey] {
		return existing, nil
	}
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.OwnerReferences = desired.OwnerReferences
	existing.Spec = desired.Spec
	if err := r.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("failed to update shard Deployment %s: %w", name, err)
	}
	logger.Info("Updated shard Deployment", "deployment", name, "portIndex", portIndex)
	return existing, nil
}

// ensureSecretTemplate renders the shard's credential SecretTemplate. It
// returns false when the Secret that realizes the SDDC's VM Operator service
// account does not exist yet.
func (r *Reconciler) ensureSecretTemplate(
	ctx context.Context,
	logger logr.Logger,
	config pkgcfg.Config,
	shard *appsv1.Deployment,
	sddcName string) (bool, error) {

	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets,
		ctrlclient.InNamespace(config.PodNamespace),
		ctrlclient.MatchingLabels{
			serviceAccountNameLabel: LabelValue(vmopServiceAccountName),
			serviceAccountSDDCLabel: LabelValue(sddcName),
		}); err != nil {
		return false, fmt.Errorf("failed to list the credential Secrets of SDDC %q: %w", sddcName, err)
	}
	switch len(secrets.Items) {
	case 0:
		logger.Info("Waiting for the credential Secret of the SDDC's VM Operator service account")
		return false, nil
	case 1:
	default:
		return false, fmt.Errorf("found %d credential Secrets for SDDC %q, expected one",
			len(secrets.Items), sddcName)
	}

	desired := RenderSecretTemplate(config.PodNamespace, sddcName, secrets.Items[0].Name, config, shard)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(SecretTemplateGVK)
	err := r.Get(ctx, ctrlclient.ObjectKeyFromObject(desired), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return false, fmt.Errorf("failed to create SecretTemplate %s: %w", desired.GetName(), err)
		}
		logger.Info("Created shard credential SecretTemplate", "secretTemplate", desired.GetName())
		return true, nil
	case err != nil:
		return false, fmt.Errorf("failed to get SecretTemplate %s: %w", desired.GetName(), err)
	}

	if equalSpec(existing, desired) {
		return true, nil
	}
	existing.Object["spec"] = desired.Object["spec"]
	existing.SetLabels(desired.GetLabels())
	existing.SetOwnerReferences(desired.GetOwnerReferences())
	if err := r.Update(ctx, existing); err != nil {
		return false, fmt.Errorf("failed to update SecretTemplate %s: %w", desired.GetName(), err)
	}
	return true, nil
}

func (r *Reconciler) deleteShard(
	ctx context.Context,
	logger logr.Logger,
	config pkgcfg.Config,
	sddcName string) error {

	d := &appsv1.Deployment{}
	d.Namespace = config.PodNamespace
	d.Name = DeploymentName(config.DeploymentName, sddcName)
	if err := r.Delete(ctx, d); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete shard Deployment %s: %w", d.Name, err)
	} else if err == nil {
		logger.Info("Deleted shard Deployment", "deployment", d.Name)
	}

	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(SecretTemplateGVK)
	tmpl.SetNamespace(config.PodNamespace)
	tmpl.SetName(CredentialsSecretName(config.VCCredsSecretName, sddcName))
	if err := r.Delete(ctx, tmpl); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete SecretTemplate %s: %w", tmpl.GetName(), err)
	}
	return nil
}

// isShardOrGlobal admits the shard Deployments and the global Deployment.
func (r *Reconciler) isShardOrGlobal(obj ctrlclient.Object) bool {
	config := pkgcfg.FromContext(r.Context)
	if obj.GetNamespace() != config.PodNamespace {
		return false
	}
	if _, ok := obj.GetLabels()[ShardLabelKey]; ok {
		return true
	}
	return obj.GetName() == config.DeploymentName
}

// sddcsForDeployment maps a shard Deployment to its SDDC, and the global
// Deployment to every SDDC, so a change to the global Deployment, such as a
// new image, is rendered into every shard.
func (r *Reconciler) sddcsForDeployment(ctx context.Context, obj ctrlclient.Object) []reconcile.Request {
	if name, ok := obj.GetAnnotations()[SDDCAnnotationKey]; ok {
		return []reconcile.Request{{NamespacedName: ctrlclient.ObjectKey{Name: name}}}
	}
	sddcs, err := multivc.ListSDDCs(ctx, r.Client)
	if err != nil {
		r.Logger.Error(err, "Failed to list SDDCs for the global Deployment")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(sddcs))
	for _, s := range sddcs {
		requests = append(requests, reconcile.Request{NamespacedName: ctrlclient.ObjectKey{Name: s.Name}})
	}
	return requests
}

func equalSpec(a, b *unstructured.Unstructured) bool {
	as, _, _ := unstructured.NestedFieldNoCopy(a.Object, "spec")
	bs, _, _ := unstructured.NestedFieldNoCopy(b.Object, "spec")
	return reflect.DeepEqual(as, bs)
}
