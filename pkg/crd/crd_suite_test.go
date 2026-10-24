// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package crd_test

import (
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/vmware-tanzu/vm-operator/pkg/constants/testlabels"
	_ "github.com/vmware-tanzu/vm-operator/test/builder/log"
	"github.com/vmware-tanzu/vm-operator/test/testutil"
)

var (
	envTestEnv    *envtest.Environment
	envTestActive bool
	envTestClient ctrlclient.Client
)

func TestCRD(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CRD Test Suite")
}

func envTestsEnabled() bool {
	return Label(testlabels.EnvTest).MatchesLabelFilter(GinkgoLabelFilter())
}

var _ = BeforeSuite(func() {
	if !envTestsEnabled() {
		return
	}
	setupComplete := false
	defer func() {
		if !setupComplete {
			if err := stopEnvTest(); err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "failed to stop envtest after setup failure: %v\n", err)
			}
		}
	}()

	rootDir := testutil.GetRootDirOrDie()
	envTestEnv = &envtest.Environment{
		BinaryAssetsDirectory: filepath.Join(rootDir, "hack", "tools", "bin", goruntime.GOOS+"_"+goruntime.GOARCH),
	}

	envTestActive = true
	cfg, err := envTestEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	scheme := runtime.NewScheme()
	Expect(apiextensionsv1.AddToScheme(scheme)).To(Succeed())

	envTestClient, err = ctrlclient.New(cfg, ctrlclient.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	setupComplete = true
})

var _ = AfterSuite(func() {
	Expect(stopEnvTest()).To(Succeed())
})

func stopEnvTest() error {
	if !envTestActive {
		return nil
	}
	err := envTestEnv.Stop()
	if err != nil {
		// Environment.Stop may return before stopping the control plane.
		if stopErr := envTestEnv.ControlPlane.Stop(); stopErr != nil {
			return errors.Join(err, fmt.Errorf("stop envtest control plane: %w", stopErr))
		}
	}
	envTestActive = false
	return err
}
