// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package vcenter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vmware/govmomi/fault"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/constants"
	"github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/vmidentity"
)

var errVMNotFound = errors.New("vm not found")

// ErrVMIdentityMismatch means a VM with the requested instance UUID exists,
// but its ownership stamps do not authorize this VirtualMachine to use it.
var ErrVMIdentityMismatch = errors.New("VM identity mismatch")

// GetVirtualMachine gets the VM from VC, either by the MoID or the Instance UUID.
func GetVirtualMachine(
	vmCtx pkgctx.VirtualMachineContext,
	vimClient *vim25.Client,
	datacenter *object.Datacenter) (*object.VirtualMachine, error) {

	var identityErr error
	if moID := vmCtx.VM.Status.UniqueID; moID != "" {
		if vm, err := findVMByMoID(vmCtx, vimClient, datacenter, moID); err == nil {
			return vm, nil
		} else if errors.Is(err, ErrVMIdentityMismatch) {
			identityErr = err
		} else if !errors.Is(err, errVMNotFound) {
			return nil, err
		}
	}

	instanceUUID := vmCtx.VM.Spec.InstanceUUID
	if instanceUUID != "" {
		if vm, err := findVMByInstancedUUID(vmCtx, vimClient, datacenter, instanceUUID); err == nil {
			return vm, nil
		} else if errors.Is(err, ErrVMIdentityMismatch) {
			identityErr = err
		} else if !errors.Is(err, errVMNotFound) {
			return nil, err
		}
	}

	if uid := string(vmCtx.VM.UID); uid != "" && instanceUUID == "" {
		// We used to set the VM's InstanceUUID to that of the k8s VM UID. Fallback
		// to that ID to still find VMs there were created with that.
		if vm, err := findVMByInstancedUUID(vmCtx, vimClient, datacenter, uid); err == nil {
			return vm, nil
		} else if errors.Is(err, ErrVMIdentityMismatch) {
			identityErr = err
		} else if !errors.Is(err, errVMNotFound) {
			return nil, err
		}
	}

	return nil, identityErr
}

func findVMByMoID(
	vmCtx pkgctx.VirtualMachineContext,
	vimClient *vim25.Client,
	datacenter *object.Datacenter,
	moID string) (*object.VirtualMachine, error) {

	moRef := vimtypes.ManagedObjectReference{
		Type:  "VirtualMachine",
		Value: moID,
	}

	vm := mo.VirtualMachine{}
	if err := property.DefaultCollector(vimClient).RetrieveOne(vmCtx, moRef, []string{"name", "config.instanceUuid", "config.extraConfig"}, &vm); err != nil {
		var f *vimtypes.ManagedObjectNotFound
		if _, ok := fault.As(err, &f); ok {
			return nil, errVMNotFound
		}
		return nil, fmt.Errorf("error retrieving VM via MoID %q: %w", moID, err)
	}

	// A MoID is unique only within one vCenter. When a VM's status was
	// written in another vCenter, the same MoID can name an unrelated VM in
	// this one. Use the VM only when its instance UUID is one this VM can have
	// and its ownership stamps agree. Otherwise fall back to UUID lookup.
	//
	// A VC VM that no VirtualMachine resource describes, for example after an
	// etcd restore, is left alone by this CR-first lookup.
	// TODO(multi-vCenter): reconcile drift between VC VMs and VirtualMachine
	// resources after an etcd restore. That is separate work.
	// TODO(multi-vCenter): scan for VC VMs stamped with this Supervisor's ID
	// that no VirtualMachine resource describes.
	if vm.Config == nil {
		return nil, fmt.Errorf("%w: VM at MoID %q has no config", ErrVMIdentityMismatch, moID)
	}
	if !instanceUUIDMatches(vmCtx, vm.Config.InstanceUuid) {
		vmCtx.Logger.Info("Ignoring VM found via MoID with another instance UUID",
			"moID", moID)
		return nil, errVMNotFound
	}
	vcVM := object.NewVirtualMachine(vimClient, moRef)
	if err := authorizeVM(vmCtx, datacenter, vcVM, vm.Config.ExtraConfig); err != nil {
		return nil, fmt.Errorf("VM at MoID %q: %w", moID, err)
	}

	vmCtx.Logger.V(4).Info("Found VM via MoID", "moID", moID)
	return vcVM, nil
}

// instanceUUIDMatches checks spec.instanceUUID, or the legacy CR UID only
// when the spec has no instance UUID.
func instanceUUIDMatches(vmCtx pkgctx.VirtualMachineContext, instanceUUID string) bool {
	if instanceUUID == "" {
		return false
	}
	if w := vmCtx.VM.Spec.InstanceUUID; w != "" {
		return strings.EqualFold(w, instanceUUID)
	}
	return vmCtx.VM.UID != "" && strings.EqualFold(string(vmCtx.VM.UID), instanceUUID)
}

// identityStamp gets one well-formed ownership value. Duplicate or non-string
// values are ambiguous and cannot be interpreted as absent legacy stamps.
func identityStamp(extraConfig []vimtypes.BaseOptionValue, key string) (string, bool, error) {
	var value string
	var found bool
	for _, base := range extraConfig {
		if base == nil || base.GetOptionValue() == nil || base.GetOptionValue().Key != key {
			continue
		}
		if found {
			return "", false, fmt.Errorf("%w: duplicate %s stamp", ErrVMIdentityMismatch, key)
		}
		found = true
		v, ok := base.GetOptionValue().Value.(string)
		if !ok || v == "" {
			return "", false, fmt.Errorf("%w: invalid %s stamp", ErrVMIdentityMismatch, key)
		}
		value = v
	}
	return value, found, nil
}

// validateVMIdentity checks ownership only after the instance UUID has been
// verified. Combined mode retains lookup of unstamped legacy VMs; shard mode
// requires complete stamps for ordinary operations.
func validateVMIdentity(
	vmCtx pkgctx.VirtualMachineContext,
	extraConfig []vimtypes.BaseOptionValue) error {

	want := vmidentity.FromContext(vmCtx)
	if pkgcfg.FromContext(vmCtx).MultiVC.IsShard() && want.SupervisorID == "" {
		return fmt.Errorf("%w: local Supervisor ID is unavailable", ErrVMIdentityMismatch)
	}
	stamped, hasSupervisor, err := identityStamp(extraConfig, vmidentity.ExtraConfigSupervisorID)
	if err != nil {
		return err
	}
	if !hasSupervisor && pkgcfg.FromContext(vmCtx).MultiVC.IsShard() {
		return fmt.Errorf("%w: missing Supervisor stamp", ErrVMIdentityMismatch)
	}
	if hasSupervisor && !vmidentity.SupervisorMatches(stamped, want.SupervisorID) {
		return fmt.Errorf("%w: Supervisor stamp does not match", ErrVMIdentityMismatch)
	}
	name, hasName, err := identityStamp(extraConfig, constants.ExtraConfigVMServiceNamespacedName)
	if err != nil {
		return err
	}
	if hasName && name != vmCtx.VM.NamespacedName() {
		return fmt.Errorf("%w: namespace/name stamp does not match", ErrVMIdentityMismatch)
	}
	if hasSupervisor && !hasName {
		return fmt.Errorf("%w: namespace/name stamp is missing", ErrVMIdentityMismatch)
	}
	org, hasOrg, err := identityStamp(extraConfig, vmidentity.ExtraConfigNamespaceOrgID)
	if err != nil {
		return err
	}
	if (hasOrg && org != want.OrgID) || (hasSupervisor && want.OrgID != "" && !hasOrg) {
		return fmt.Errorf("%w: organization stamp does not match", ErrVMIdentityMismatch)
	}
	return nil
}

func authorizeVM(
	vmCtx pkgctx.VirtualMachineContext,
	datacenter *object.Datacenter,
	vcVM *object.VirtualMachine,
	extraConfig []vimtypes.BaseOptionValue) error {

	err := validateVMIdentity(vmCtx, extraConfig)
	if err == nil || !vmidentity.AdoptionRequested(vmCtx) {
		return err
	}
	return adoptLegacyVM(vmCtx, datacenter, vcVM)
}

// adoptLegacyVM is the sole unstamped-VM exception. It is reached only from
// create/update with explicit import or restore intent, and stamps before the
// candidate can be used by ordinary reconciliation.
func adoptLegacyVM(
	vmCtx pkgctx.VirtualMachineContext,
	datacenter *object.Datacenter,
	vcVM *object.VirtualMachine) error {
	if !pkgcfg.FromContext(vmCtx).MultiVC.IsShard() ||
		!vmCtx.VM.DeletionTimestamp.IsZero() ||
		vmidentity.FromContext(vmCtx).SupervisorID == "" {
		return fmt.Errorf("%w: legacy adoption is not permitted", ErrVMIdentityMismatch)
	}
	_, imported := vmCtx.VM.Annotations[vmopv1.ImportedVMAnnotation]
	_, restored := vmCtx.VM.Annotations[vmopv1.RestoredVMAnnotation]
	if !imported && !restored {
		return fmt.Errorf("%w: legacy adoption has no import or restore provenance", ErrVMIdentityMismatch)
	}

	var observed mo.VirtualMachine
	if err := vcVM.Properties(vmCtx, vcVM.Reference(),
		[]string{"config.instanceUuid", "config.extraConfig", "config.changeVersion"}, &observed); err != nil {
		return fmt.Errorf("reading legacy VM before adoption: %w", err)
	}
	if observed.Config == nil || !instanceUUIDMatches(vmCtx, observed.Config.InstanceUuid) {
		return fmt.Errorf("%w: legacy VM instance UUID changed", ErrVMIdentityMismatch)
	}
	refs, err := object.NewSearchIndex(vcVM.Client()).FindAllByUuid(
		vmCtx, datacenter, observed.Config.InstanceUuid, true, vimtypes.NewBool(true))
	if err != nil {
		return fmt.Errorf("checking legacy VM instance UUID uniqueness: %w", err)
	}
	if len(refs) != 1 || refs[0].Reference() != vcVM.Reference() {
		return fmt.Errorf("%w: legacy VM instance UUID is ambiguous", ErrVMIdentityMismatch)
	}
	if err := validateVMIdentity(vmCtx, observed.Config.ExtraConfig); err == nil {
		return nil
	}
	want := vmidentity.FromContext(vmCtx)
	if _, present, err := identityStamp(observed.Config.ExtraConfig, vmidentity.ExtraConfigSupervisorID); err != nil {
		return err
	} else if present {
		return fmt.Errorf("%w: legacy VM has a Supervisor stamp", ErrVMIdentityMismatch)
	}

	var additions []vimtypes.BaseOptionValue
	for _, field := range []struct{ key, value string }{
		{constants.ExtraConfigVMServiceNamespacedName, vmCtx.VM.NamespacedName()},
		{vmidentity.ExtraConfigNamespaceOrgID, want.OrgID},
		{vmidentity.ExtraConfigSupervisorID, want.SupervisorID},
	} {
		value, present, err := identityStamp(observed.Config.ExtraConfig, field.key)
		if err != nil {
			return err
		}
		if present {
			if value != field.value {
				return fmt.Errorf("%w: %s stamp does not match", ErrVMIdentityMismatch, field.key)
			}
			continue
		}
		if field.value != "" {
			additions = append(additions, &vimtypes.OptionValue{Key: field.key, Value: field.value})
		}
	}

	task, err := vcVM.Reconfigure(vmCtx, vimtypes.VirtualMachineConfigSpec{
		ChangeVersion: observed.Config.ChangeVersion,
		ExtraConfig:   additions,
	})
	if err != nil {
		return fmt.Errorf("stamping legacy VM ownership: %w", err)
	}
	if err := task.Wait(vmCtx); err != nil {
		return fmt.Errorf("waiting for legacy VM ownership stamp: %w", err)
	}
	var updated mo.VirtualMachine
	if err := vcVM.Properties(vmCtx, vcVM.Reference(),
		[]string{"config.instanceUuid", "config.extraConfig"}, &updated); err != nil {
		return fmt.Errorf("rereading legacy VM after adoption: %w", err)
	}
	if updated.Config == nil || !instanceUUIDMatches(vmCtx, updated.Config.InstanceUuid) {
		return fmt.Errorf("%w: adopted VM instance UUID changed", ErrVMIdentityMismatch)
	}
	if err := validateVMIdentity(vmCtx, updated.Config.ExtraConfig); err != nil {
		return fmt.Errorf("verifying adopted VM ownership: %w", err)
	}
	vmCtx.Logger.Info("Adopted legacy VM with ownership stamps", "moID", vcVM.Reference().Value)
	return nil
}

func findVMByInstancedUUID(
	vmCtx pkgctx.VirtualMachineContext,
	vimClient *vim25.Client,
	datacenter *object.Datacenter,
	uuid string) (*object.VirtualMachine, error) {

	refs, err := object.NewSearchIndex(vimClient).FindAllByUuid(vmCtx, datacenter, uuid, true, vimtypes.NewBool(true))
	if err != nil {
		return nil, fmt.Errorf("error finding VM by instance UUID %q: %w", uuid, err)
	}

	switch len(refs) {
	case 0:
		return nil, errVMNotFound
	case 1:
		vm, ok := refs[0].(*object.VirtualMachine)
		if !ok {
			return nil, fmt.Errorf("found VM reference was not a VirtualMachine but a %T", refs[0])
		}
		var moVM mo.VirtualMachine
		if err := vm.Properties(vmCtx, vm.Reference(), []string{"config.extraConfig"}, &moVM); err != nil {
			return nil, fmt.Errorf("error retrieving VM with instance UUID %q: %w", uuid, err)
		}
		if moVM.Config == nil {
			return nil, fmt.Errorf("%w: VM with instance UUID %q has no config", ErrVMIdentityMismatch, uuid)
		}
		if err := authorizeVM(vmCtx, datacenter, vm, moVM.Config.ExtraConfig); err != nil {
			return nil, fmt.Errorf("VM with instance UUID %q: %w", uuid, err)
		}
		vmCtx.Logger.V(4).Info("Found VM via instance UUID", "uuid", uuid)
		return vm, nil
	default:
		return nil, fmt.Errorf("found multiple VMs for instance UUID %q (%d): %w",
			uuid, len(refs), ErrVMIdentityMismatch)
	}
}
