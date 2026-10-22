// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EntitlementVerifier verifies that an SDDC resource is entitled to a shard.
//
// The installer is to sign the fields of the entitlement resources it writes,
// such as SDDCs and AvailabilityZones, and consumers are to verify the
// signature over the signed fields only. A shard is rendered only for an SDDC
// that passes Verify.
type EntitlementVerifier interface {
	// Verify returns nil when the SDDC is entitled, and an error that says why
	// otherwise.
	Verify(ctx context.Context, sddc *unstructured.Unstructured) error
}

// NewAllowAllVerifier returns the EntitlementVerifier to use until SDDC
// signing exists. It accepts every SDDC, and logs once per SDDC that the SDDC
// was not verified.
func NewAllowAllVerifier(logger logr.Logger) EntitlementVerifier {
	return &allowAllVerifier{logger: logger}
}

type allowAllVerifier struct {
	logger logr.Logger
	logged sync.Map
}

func (v *allowAllVerifier) Verify(_ context.Context, sddc *unstructured.Unstructured) error {
	if _, loaded := v.logged.LoadOrStore(sddc.GetName(), struct{}{}); !loaded {
		v.logger.Info("SDDC entitlement is not verified: signature verification is not implemented",
			"sddc", sddc.GetName())
	}
	return nil
}
