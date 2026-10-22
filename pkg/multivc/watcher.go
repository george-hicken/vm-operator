// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
)

// ErrTopologyChanged is returned by the topology watcher when the shard's
// zones, the primary SDDC, or the shard's vCenter endpoint changed. The shard
// exits, and the restarted process builds its cache selector and vCenter
// client from the new topology.
var ErrTopologyChanged = errors.New("multi-vCenter topology changed; restarting the shard")

// DefaultTopologyPollInterval is how often the topology watcher reads the
// SDDCs and AvailabilityZones.
const DefaultTopologyPollInterval = 15 * time.Second

// NewTopologyWatcher returns a runnable that reads the shard's topology every
// interval and returns ErrTopologyChanged once its fingerprint differs from
// startFingerprint. A failed read is logged and retried at the next interval.
//
// Restarting on a change keeps the cache selector a constant of the process,
// rather than rebuilding informers while controllers use them.
func NewTopologyWatcher(
	reader ctrlclient.Reader,
	shardSDDC, startFingerprint string,
	interval time.Duration,
	logger logr.Logger) ctrlmgr.RunnableFunc {

	return func(ctx context.Context) error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
			t, err := LoadTopology(ctx, reader, shardSDDC)
			if err != nil {
				logger.Error(err, "Failed to read the multi-vCenter topology", "sddc", shardSDDC)
				continue
			}
			if fp := t.Fingerprint(); fp != startFingerprint {
				logger.Info("Multi-vCenter topology changed",
					"sddc", shardSDDC, "primary", t.Primary,
					"ownedZones", t.Owned, "foreignZones", t.Foreign)
				return ErrTopologyChanged
			}
		}
	}
}
