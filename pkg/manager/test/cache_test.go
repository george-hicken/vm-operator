// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package manager_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"

	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	pkgcfg "github.com/vmware-tanzu/vm-operator/pkg/config"
	pkgmgr "github.com/vmware-tanzu/vm-operator/pkg/manager"
	"github.com/vmware-tanzu/vm-operator/test/builder"
)

func cacheTests() {
	var (
		ctx    *builder.IntegrationTestContext
		client ctrlclient.Client
		cache  *cacheProxy
	)

	BeforeEach(func() {
		ctx = suite.NewIntegrationTestContext()
		client = suite.GetManager().GetClient()
		Expect(suite.GetManager().GetCache()).To(BeAssignableToTypeOf(&cacheProxy{}))
		cache = suite.GetManager().GetCache().(*cacheProxy)
	})

	AfterEach(func() {
		ctx.AfterEach()
		ctx = nil
	})

	Context("Getting objects", func() {
		var (
			obj    ctrlclient.Object
			objKey ctrlclient.ObjectKey
		)
		JustBeforeEach(func() {
			obj.SetGenerateName("my-object-")
			obj.SetNamespace(ctx.Namespace)
			Expect(client.Create(ctx, obj)).To(Succeed())
			objKey = ctrlclient.ObjectKeyFromObject(obj)
		})

		Context("ConfigMap", func() {
			BeforeEach(func() {
				obj = &corev1.ConfigMap{}
			})
			It("should return the object with a live read", func() {
				Expect(client.Get(ctx, objKey, obj)).To(Succeed())
				Expect(cache.getCallCountFor(obj)).To(Equal(0))
			})
		})
		Context("Node", func() {
			BeforeEach(func() {
				obj = &corev1.Node{}
			})
			It("should return the object with a live read", func() {
				Expect(client.Get(ctx, objKey, obj)).To(Succeed())
				Expect(cache.getCallCountFor(obj)).To(Equal(1))
			})
		})
		Context("Secret", func() {
			BeforeEach(func() {
				obj = &corev1.Secret{}
			})
			It("should return the object with a live read", func() {
				Expect(client.Get(ctx, objKey, obj)).To(Succeed())
				Expect(cache.getCallCountFor(obj)).To(Equal(0))
			})
		})
	})

	It("watches global and shard Deployments with namespace-only authorization", func() {
		const serviceAccountName = "cache-scope-test"
		podNamespace := ctx.Namespace
		admin := ctx.Client
		Expect(admin.Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: podNamespace},
		})).To(Succeed())
		Expect(admin.Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: podNamespace},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"apps"}, Resources: []string{"deployments"},
				Verbs: []string{"get", "list", "watch"},
			}},
		})).To(Succeed())
		Expect(admin.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: podNamespace},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName, Kind: "Role", Name: serviceAccountName,
			},
			Subjects: []rbacv1.Subject{{
				Kind: "ServiceAccount", Name: serviceAccountName, Namespace: podNamespace,
			}},
		})).To(Succeed())

		restrictedConfig := rest.CopyConfig(suite.GetEnvTestConfig())
		restrictedConfig.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", podNamespace, serviceAccountName),
			Groups: []string{
				"system:serviceaccounts",
				"system:serviceaccounts:" + podNamespace,
				"system:authenticated",
			},
		}
		restricted, err := ctrlclient.New(restrictedConfig, ctrlclient.Options{Scheme: suite.GetManager().GetScheme()})
		Expect(err).NotTo(HaveOccurred())
		var deployments appsv1.DeploymentList
		Expect(restricted.List(ctx, &deployments, ctrlclient.InNamespace(podNamespace))).To(Succeed())
		Expect(apierrors.IsForbidden(restricted.List(ctx, &deployments))).To(BeTrue())

		managerContext := pkgcfg.WithConfig(pkgcfg.Config{
			MultiVC: pkgcfg.MultiVC{Mode: pkgcfg.MultiVCModeGlobal},
		})
		mgr, err := pkgmgr.New(managerContext, pkgmgr.Options{
			KubeConfig:          restrictedConfig,
			PodNamespace:        podNamespace,
			MetricsAddr:         "0",
			InitializeProviders: pkgmgr.InitializeProvidersNoopFn,
			AddToManager:        pkgmgr.AddToManagerNoopFn,
		})
		Expect(err).NotTo(HaveOccurred())

		informer, err := mgr.GetCache().GetInformer(ctx, &appsv1.Deployment{})
		Expect(err).NotTo(HaveOccurred())
		events := make(chan ctrlclient.ObjectKey, 10)
		_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				deployment := obj.(*appsv1.Deployment)
				events <- ctrlclient.ObjectKeyFromObject(deployment)
			},
		})
		Expect(err).NotTo(HaveOccurred())
		cacheContext, cancel := context.WithCancel(ctx)
		cacheDone := make(chan error, 1)
		go func() { cacheDone <- mgr.GetCache().Start(cacheContext) }()
		DeferCleanup(func() {
			cancel()
			Eventually(cacheDone).Should(Receive(BeNil()))
		})
		Eventually(informer.HasSynced, 10*time.Second).Should(BeTrue())

		newDeployment := func(name, namespace string) *appsv1.Deployment {
			labels := map[string]string{"app": name}
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name: "test", Image: "example.invalid/test:latest",
						}}},
					},
				},
			}
		}
		for _, name := range []string{"global", "shard"} {
			deployment := newDeployment(name, podNamespace)
			Expect(admin.Create(ctx, deployment)).To(Succeed())
			Eventually(events).Should(Receive(Equal(ctrlclient.ObjectKeyFromObject(deployment))))
		}

		foreign := newDeployment("cache-scope-"+podNamespace, ctx.PodNamespace)
		Expect(admin.Create(ctx, foreign)).To(Succeed())
		DeferCleanup(func() { Expect(admin.Delete(context.Background(), foreign)).To(Succeed()) })
		Consistently(events, time.Second).ShouldNot(Receive())
		Expect(mgr.GetCache().List(ctx, &deployments)).To(Succeed())
		Expect(deployments.Items).To(HaveLen(2))
		for _, deployment := range deployments.Items {
			Expect(deployment.Namespace).To(Equal(podNamespace))
		}
	})

	It("keeps the combined cache defaults", func() {
		var cacheOptions ctrlcache.Options
		_, err := pkgmgr.New(pkgcfg.NewContext(), pkgmgr.Options{
			KubeConfig:          suite.GetEnvTestConfig(),
			PodNamespace:        ctx.Namespace,
			MetricsAddr:         "0",
			InitializeProviders: pkgmgr.InitializeProvidersNoopFn,
			AddToManager:        pkgmgr.AddToManagerNoopFn,
			NewCache: func(config *rest.Config, opts ctrlcache.Options) (ctrlcache.Cache, error) {
				cacheOptions = opts
				return ctrlcache.New(config, opts)
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(cacheOptions.DefaultNamespaces).To(BeNil())
		Expect(cacheOptions.ByObject).To(BeNil())
	})
}

type cacheProxy struct {
	sync.Mutex
	ctrlcache.Cache
	getCalls map[ctrlclient.Object]int
}

func newCacheProxy(
	config *rest.Config,
	opts ctrlcache.Options) (ctrlcache.Cache, error) {

	cache, err := ctrlcache.New(config, opts)
	if err != nil {
		return nil, err
	}

	return &cacheProxy{
		Cache:    cache,
		getCalls: map[ctrlclient.Object]int{},
	}, nil
}

func (c *cacheProxy) getCallCountFor(obj ctrlclient.Object) int {
	c.Lock()
	defer c.Unlock()
	return c.getCalls[obj]
}

func (c *cacheProxy) Get(
	ctx context.Context,
	key ctrlclient.ObjectKey,
	obj ctrlclient.Object,
	opts ...ctrlclient.GetOption) error {

	c.Lock()
	defer c.Unlock()

	if i, ok := c.getCalls[obj]; ok {
		c.getCalls[obj] = i + 1
	} else {
		c.getCalls[obj] = 1
	}

	return c.Cache.Get(ctx, key, obj, opts...)
}
