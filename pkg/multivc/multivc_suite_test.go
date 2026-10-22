// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package multivc_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMultiVC(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multi-vCenter Suite")
}
