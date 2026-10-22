// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// DependentPredicate returns the predicate that admits a resource that
// depends on a VM, such as a snapshot or a web console request, to the shard
// that is responsible for the VM. vmName returns the name of the VM in the
// resource's namespace.
//
// A shard's cache holds only its own VMs, so the resource is admitted when its
// VM is in the cache. A resource whose VM does not exist, or that names no VM,
// is admitted by every shard, so that it is still reconciled, for example to
// remove its finalizer. Outside shard mode every resource is admitted.
//
// The predicate is evaluated on events. A resource created before its VM has
// a zone is reconciled once its VM's shard receives an event for it, at the
// latest at the next resync.
func DependentPredicate(
	m pkgcfg.MultiVC,
	cached, live ctrlclient.Reader,
	vmName func(ctrlclient.Object) string) predicate.Predicate {

	if !m.IsShard() || m.Sole {
		return predicate.NewPredicateFuncs(func(ctrlclient.Object) bool { return true })
	}

	return predicate.NewPredicateFuncs(func(obj ctrlclient.Object) bool {
		return ownsDependent(context.Background(), cached, live, obj.GetNamespace(), vmName(obj))
	})
}

func ownsDependent(
	ctx context.Context,
	cached, live ctrlclient.Reader,
	namespace, name string) bool {

	if name == "" {
		return true
	}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}
	if err := cached.Get(ctx, key, &vmopv1.VirtualMachine{}); err == nil {
		return true
	}
	err := live.Get(ctx, key, &vmopv1.VirtualMachine{})
	return apierrors.IsNotFound(err)
}

// ZonePredicate returns the predicate that admits a Zone or AvailabilityZone
// to the process that owns it. Outside shard mode every zone is admitted.
func ZonePredicate(m pkgcfg.MultiVC) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj ctrlclient.Object) bool {
		return m.OwnsZone(obj.GetName())
	})
}
