// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package vsphere

import (
	pkgctx "github.com/vmware-tanzu/vm-operator/pkg/context"
	"github.com/vmware-tanzu/vm-operator/pkg/providers"
	vcclient "github.com/vmware-tanzu/vm-operator/pkg/providers/vsphere/client"
)

// CreateGetSourceFilePathsForTest runs vmCreateGetSourceFilePaths on p using
// vcClient, while primary is the provider's own vCenter client. It lets tests
// exercise a non-primary vCenter.
func CreateGetSourceFilePathsForTest(
	p providers.VirtualMachineProviderInterface,
	primary, vcClient *vcclient.Client,
	vmCtx pkgctx.VirtualMachineContext,
	createArgs *VMCreateArgs) error {

	vs := p.(*vSphereVMProvider)
	vs.vcClient = primary
	return vs.vmCreateGetSourceFilePaths(vmCtx, vcClient, createArgs)
}
