// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/vmware/govmomi/vapi/library"
	"github.com/vmware/govmomi/vapi/rest"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
)

// NeedsLibraryItemCheck returns true when a shard must check that a content
// library item is in its own vCenter before it acts on a resource for the
// item. That is in shard mode once several SDDCs exist.
func NeedsLibraryItemCheck(m pkgcfg.MultiVC) bool {
	return m.IsShard() && !m.Sole
}

// LibraryItemExists returns true when the content library item is in the
// vCenter of the REST client.
//
// Until content library items record their SDDC, a shard associates an item
// with its vCenter by finding it there. Library item IDs are UUIDs, so an item
// of another vCenter is not found.
func LibraryItemExists(ctx context.Context, c *rest.Client, itemID string) (bool, error) {
	if _, err := library.NewManager(c).GetLibraryItem(ctx, itemID); err != nil {
		if rest.IsStatusError(err, http.StatusNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get library item %q: %w", itemID, err)
	}
	return true, nil
}

// RESTClientFunc returns a REST client for the shard's vCenter.
type RESTClientFunc func(context.Context) (*rest.Client, error)

// SkipLibraryItem returns true when the shard must leave the content library
// item to another shard: once several SDDCs exist, and the item is not in the
// shard's vCenter. An ID that is a VM MoID cannot be told apart across
// vCenters, so it is not skipped.
func SkipLibraryItem(
	ctx context.Context,
	m pkgcfg.MultiVC,
	restClient RESTClientFunc,
	itemID string) (bool, error) {

	if !NeedsLibraryItemCheck(m) || itemID == "" || strings.HasPrefix(itemID, "vm-") {
		return false, nil
	}
	c, err := restClient(ctx)
	if err != nil {
		return false, err
	}
	exists, err := LibraryItemExists(ctx, c, itemID)
	if err != nil {
		return false, err
	}
	return !exists, nil
}
