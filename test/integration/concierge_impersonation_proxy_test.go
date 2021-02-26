// Copyright 2020-2021 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8sinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"go.pinniped.dev/internal/concierge/impersonator"
	"go.pinniped.dev/internal/testutil/impersonationtoken"
	"go.pinniped.dev/test/library"
)

const (
	// TODO don't hard code "pinniped-concierge-" in these strings. It should be constructed from the env app name.
	impersonationProxyConfigMapName    = "pinniped-concierge-impersonation-proxy-config"
	impersonationProxyTLSSecretName    = "pinniped-concierge-impersonation-proxy-tls-serving-certificate" //nolint:gosec // this is not a credential
	impersonationProxyLoadBalancerName = "pinniped-concierge-impersonation-proxy-load-balancer"
)

// Note that this test supports being run on all of our integration test cluster types:
//   - load balancers not supported, has squid proxy (e.g. kind)
//   - load balancers supported, has squid proxy (e.g. EKS)
//   - load balancers supported, no squid proxy (e.g. GKE)
func TestImpersonationProxy(t *testing.T) {
	env := library.IntegrationEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Create a client using the admin kubeconfig.
	adminClient := library.NewKubernetesClientset(t)

	// Create a WebhookAuthenticator.
	authenticator := library.CreateTestWebhookAuthenticator(ctx, t)

	// The address of the ClusterIP service that points at the impersonation proxy's port (used when there is no load balancer).
	proxyServiceEndpoint := fmt.Sprintf("%s-proxy.%s.svc.cluster.local", env.ConciergeAppName, env.ConciergeNamespace)
	// The error message that will be returned by squid when the impersonation proxy port inside the cluster is not listening.
	serviceUnavailableViaSquidError := fmt.Sprintf(`Get "https://%s/api/v1/namespaces": Service Unavailable`, proxyServiceEndpoint)

	impersonationProxyViaSquidClient := func(caData []byte) *kubernetes.Clientset {
		t.Helper()
		kubeconfig := &rest.Config{
			Host:            fmt.Sprintf("https://%s", proxyServiceEndpoint),
			TLSClientConfig: rest.TLSClientConfig{Insecure: caData == nil, CAData: caData},
			BearerToken:     impersonationtoken.Make(t, env.TestUser.Token, &authenticator, env.APIGroupSuffix),
			Proxy: func(req *http.Request) (*url.URL, error) {
				proxyURL, err := url.Parse(env.Proxy)
				require.NoError(t, err)
				t.Logf("passing request for %s through proxy %s", req.URL, proxyURL.String())
				return proxyURL, nil
			},
		}
		impersonationProxyClient, err := kubernetes.NewForConfig(kubeconfig)
		require.NoError(t, err, "unexpected failure from kubernetes.NewForConfig()")
		return impersonationProxyClient
	}

	impersonationProxyViaLoadBalancerClient := func(host string, caData []byte) *kubernetes.Clientset {
		t.Helper()
		kubeconfig := &rest.Config{
			Host:            fmt.Sprintf("https://%s", host),
			TLSClientConfig: rest.TLSClientConfig{Insecure: caData == nil, CAData: caData},
			BearerToken:     impersonationtoken.Make(t, env.TestUser.Token, &authenticator, env.APIGroupSuffix),
		}
		impersonationProxyClient, err := kubernetes.NewForConfig(kubeconfig)
		require.NoError(t, err, "unexpected failure from kubernetes.NewForConfig()")
		return impersonationProxyClient
	}

	oldConfigMap, err := adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Get(ctx, impersonationProxyConfigMapName, metav1.GetOptions{})
	if !k8serrors.IsNotFound(err) {
		require.NoError(t, err) // other errors aside from NotFound are unexpected
		t.Logf("stashing a pre-existing configmap %s", oldConfigMap.Name)
		require.NoError(t, adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Delete(ctx, impersonationProxyConfigMapName, metav1.DeleteOptions{}))
	}

	impersonationProxyLoadBalancerIngress := ""

	if env.HasCapability(library.HasExternalLoadBalancerProvider) { //nolint:nestif // come on... it's just a test
		// Check that load balancer has been created.
		library.RequireEventuallyWithoutError(t, func() (bool, error) {
			return hasImpersonationProxyLoadBalancerService(ctx, adminClient, env.ConciergeNamespace)
		}, 10*time.Second, 500*time.Millisecond)

		// Wait for the load balancer to get an ingress and make a note of its address.
		var ingress *corev1.LoadBalancerIngress
		library.RequireEventuallyWithoutError(t, func() (bool, error) {
			ingress, err = getImpersonationProxyLoadBalancerIngress(ctx, adminClient, env.ConciergeNamespace)
			if err != nil {
				return false, err
			}
			return ingress != nil, nil
		}, 10*time.Second, 500*time.Millisecond)
		if ingress.Hostname != "" {
			impersonationProxyLoadBalancerIngress = ingress.Hostname
		} else {
			require.NotEmpty(t, ingress.IP, "the ingress should have either a hostname or IP, but it didn't")
			impersonationProxyLoadBalancerIngress = ingress.IP
		}
	} else {
		require.NotEmpty(t, env.Proxy,
			"test cluster does not support load balancers but also doesn't have a squid proxy... "+
				"this is not a supported configuration for test clusters")

		// Check that no load balancer has been created.
		library.RequireNeverWithoutError(t, func() (bool, error) {
			return hasImpersonationProxyLoadBalancerService(ctx, adminClient, env.ConciergeNamespace)
		}, 10*time.Second, 500*time.Millisecond)

		// Check that we can't use the impersonation proxy to execute kubectl commands yet.
		_, err = impersonationProxyViaSquidClient(nil).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		require.EqualError(t, err, serviceUnavailableViaSquidError)

		// Create configuration to make the impersonation proxy turn on with a hard coded endpoint (without a LoadBalancer).
		configMap := configMapForConfig(t, impersonator.Config{
			Mode:     impersonator.ModeEnabled,
			Endpoint: proxyServiceEndpoint,
			TLS:      nil,
		})
		t.Logf("creating configmap %s", configMap.Name)
		_, err = adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Create(ctx, &configMap, metav1.CreateOptions{})
		require.NoError(t, err)

		// At the end of the test, clean up the ConfigMap.
		t.Cleanup(func() {
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			t.Logf("cleaning up configmap at end of test %s", impersonationProxyConfigMapName)
			err = adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Delete(ctx, impersonationProxyConfigMapName, metav1.DeleteOptions{})
			require.NoError(t, err)

			if len(oldConfigMap.Data) != 0 {
				t.Log(oldConfigMap)
				oldConfigMap.UID = "" // cant have a UID yet
				oldConfigMap.ResourceVersion = ""
				t.Logf("restoring a pre-existing configmap %s", oldConfigMap.Name)
				_, err = adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Create(ctx, oldConfigMap, metav1.CreateOptions{})
				require.NoError(t, err)
			}
		})
	}

	// Wait for ca data to be available at the secret location.
	var caSecret *corev1.Secret
	require.Eventually(t,
		func() bool {
			caSecret, err = adminClient.CoreV1().Secrets(env.ConciergeNamespace).Get(ctx, impersonationProxyTLSSecretName, metav1.GetOptions{})
			return caSecret != nil && caSecret.Data["ca.crt"] != nil
		}, 5*time.Minute, 250*time.Millisecond)

	// Create an impersonation proxy client with that CA data to use for the rest of this test.
	// This client performs TLS checks, so it also provides test coverage that the impersonation proxy server is generating TLS certs correctly.
	var impersonationProxyClient *kubernetes.Clientset
	if env.HasCapability(library.HasExternalLoadBalancerProvider) {
		impersonationProxyClient = impersonationProxyViaLoadBalancerClient(impersonationProxyLoadBalancerIngress, caSecret.Data["ca.crt"])
	} else {
		impersonationProxyClient = impersonationProxyViaSquidClient(caSecret.Data["ca.crt"])
	}

	// Test that the user can perform basic actions through the client with their username and group membership
	// influencing RBAC checks correctly.
	t.Run(
		"access as user",
		library.AccessAsUserTest(ctx, env.TestUser.ExpectedUsername, impersonationProxyClient),
	)
	for _, group := range env.TestUser.ExpectedGroups {
		group := group
		t.Run(
			"access as group "+group,
			library.AccessAsGroupTest(ctx, group, impersonationProxyClient),
		)
	}

	// Try more Kube API verbs through the impersonation proxy.
	t.Run("watching all the basic verbs", func(t *testing.T) {
		// Create a namespace, because it will be easier to exercise deletecollection if we have a namespace.
		namespace, err := adminClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "impersonation-integration-test-"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		// Schedule the namespace for cleanup.
		t.Cleanup(func() {
			t.Logf("cleaning up test namespace %s", namespace.Name)
			err = adminClient.CoreV1().Namespaces().Delete(context.Background(), namespace.Name, metav1.DeleteOptions{})
			require.NoError(t, err)
		})

		// Create an RBAC rule to allow this user to read/write everything.
		library.CreateTestClusterRoleBinding(
			t,
			rbacv1.Subject{
				Kind:     rbacv1.UserKind,
				APIGroup: rbacv1.GroupName,
				Name:     env.TestUser.ExpectedUsername,
			},
			rbacv1.RoleRef{
				Kind:     "ClusterRole",
				APIGroup: rbacv1.GroupName,
				Name:     "cluster-admin",
			},
		)
		// Wait for the above RBAC rule to take effect.
		library.WaitForUserToHaveAccess(t, env.TestUser.ExpectedUsername, []string{}, &v1.ResourceAttributes{
			Namespace: namespace.Name,
			Verb:      "create",
			Group:     "",
			Version:   "v1",
			Resource:  "configmaps",
		})

		// Create and start informer to exercise the "watch" verb for us.
		informerFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
			impersonationProxyClient,
			0,
			k8sinformers.WithNamespace(namespace.Name))
		informer := informerFactory.Core().V1().ConfigMaps()
		informer.Informer() // makes sure that the informer will cache
		stopChannel := make(chan struct{})
		informerFactory.Start(stopChannel)
		t.Cleanup(func() {
			// Shut down the informer.
			close(stopChannel)
		})
		informerFactory.WaitForCacheSync(ctx.Done())

		// Use labels on our created ConfigMaps to avoid accidentally listing other ConfigMaps that might
		// exist in the namespace. In Kube 1.20+ there is a default ConfigMap in every namespace.
		configMapLabels := labels.Set{
			"pinniped.dev/testConfigMap": library.RandHex(t, 8),
		}

		// Test "create" verb through the impersonation proxy.
		_, err = impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Create(ctx,
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "configmap-1", Labels: configMapLabels}},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)
		_, err = impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Create(ctx,
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "configmap-2", Labels: configMapLabels}},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)
		_, err = impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Create(ctx,
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "configmap-3", Labels: configMapLabels}},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		// Make sure that all of the created ConfigMaps show up in the informer's cache to
		// demonstrate that the informer's "watch" verb is working through the impersonation proxy.
		require.Eventually(t, func() bool {
			_, err1 := informer.Lister().ConfigMaps(namespace.Name).Get("configmap-1")
			_, err2 := informer.Lister().ConfigMaps(namespace.Name).Get("configmap-2")
			_, err3 := informer.Lister().ConfigMaps(namespace.Name).Get("configmap-3")
			return err1 == nil && err2 == nil && err3 == nil
		}, 10*time.Second, 50*time.Millisecond)

		// Test "get" verb through the impersonation proxy.
		configMap3, err := impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Get(ctx, "configmap-3", metav1.GetOptions{})
		require.NoError(t, err)

		// Test "list" verb through the impersonation proxy.
		listResult, err := impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{
			LabelSelector: configMapLabels.String(),
		})
		require.NoError(t, err)
		require.Len(t, listResult.Items, 3)

		// Test "update" verb through the impersonation proxy.
		configMap3.Data = map[string]string{"foo": "bar"}
		updateResult, err := impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Update(ctx, configMap3, metav1.UpdateOptions{})
		require.NoError(t, err)
		require.Equal(t, "bar", updateResult.Data["foo"])

		// Make sure that the updated ConfigMap shows up in the informer's cache.
		require.Eventually(t, func() bool {
			configMap, err := informer.Lister().ConfigMaps(namespace.Name).Get("configmap-3")
			return err == nil && configMap.Data["foo"] == "bar"
		}, 10*time.Second, 50*time.Millisecond)

		// Test "patch" verb through the impersonation proxy.
		patchResult, err := impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Patch(ctx,
			"configmap-3",
			types.MergePatchType,
			[]byte(`{"data":{"baz":"42"}}`),
			metav1.PatchOptions{},
		)
		require.NoError(t, err)
		require.Equal(t, "bar", patchResult.Data["foo"])
		require.Equal(t, "42", patchResult.Data["baz"])

		// Make sure that the patched ConfigMap shows up in the informer's cache.
		require.Eventually(t, func() bool {
			configMap, err := informer.Lister().ConfigMaps(namespace.Name).Get("configmap-3")
			return err == nil && configMap.Data["foo"] == "bar" && configMap.Data["baz"] == "42"
		}, 10*time.Second, 50*time.Millisecond)

		// Test "delete" verb through the impersonation proxy.
		err = impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).Delete(ctx, "configmap-3", metav1.DeleteOptions{})
		require.NoError(t, err)

		// Make sure that the deleted ConfigMap shows up in the informer's cache.
		require.Eventually(t, func() bool {
			_, getErr := informer.Lister().ConfigMaps(namespace.Name).Get("configmap-3")
			list, listErr := informer.Lister().ConfigMaps(namespace.Name).List(configMapLabels.AsSelector())
			return k8serrors.IsNotFound(getErr) && listErr == nil && len(list) == 2
		}, 10*time.Second, 50*time.Millisecond)

		// Test "deletecollection" verb through the impersonation proxy.
		err = impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
		require.NoError(t, err)

		// Make sure that the deleted ConfigMaps shows up in the informer's cache.
		require.Eventually(t, func() bool {
			list, listErr := informer.Lister().ConfigMaps(namespace.Name).List(configMapLabels.AsSelector())
			return listErr == nil && len(list) == 0
		}, 10*time.Second, 50*time.Millisecond)

		// There should be no ConfigMaps left.
		listResult, err = impersonationProxyClient.CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{
			LabelSelector: configMapLabels.String(),
		})
		require.NoError(t, err)
		require.Len(t, listResult.Items, 0)
	})

	// Update configuration to force the proxy to disabled mode
	configMap := configMapForConfig(t, impersonator.Config{Mode: impersonator.ModeDisabled})
	if env.HasCapability(library.HasExternalLoadBalancerProvider) {
		t.Logf("creating configmap %s", configMap.Name)
		_, err = adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Create(ctx, &configMap, metav1.CreateOptions{})
		require.NoError(t, err)
	} else {
		t.Logf("updating configmap %s", configMap.Name)
		_, err = adminClient.CoreV1().ConfigMaps(env.ConciergeNamespace).Update(ctx, &configMap, metav1.UpdateOptions{})
		require.NoError(t, err)
	}

	if env.HasCapability(library.HasExternalLoadBalancerProvider) {
		// The load balancer should not exist after we disable the impersonation proxy.
		// Note that this can take kind of a long time on real cloud providers (e.g. ~22 seconds on EKS).
		library.RequireEventuallyWithoutError(t, func() (bool, error) {
			hasService, err := hasImpersonationProxyLoadBalancerService(ctx, adminClient, env.ConciergeNamespace)
			return !hasService, err
		}, time.Minute, 500*time.Millisecond)
	}

	// Check that the impersonation proxy port has shut down.
	// Ideally we could always check that the impersonation proxy's port has shut down, but on clusters where we
	// do not run the squid proxy we have no easy way to see beyond the load balancer to see inside the cluster,
	// so we'll skip this check on clusters which have load balancers but don't run the squid proxy.
	// The other cluster types that do run the squid proxy will give us sufficient coverage here.
	if env.Proxy != "" {
		require.Eventually(t, func() bool {
			// It's okay if this returns RBAC errors because this user has no role bindings.
			// What we want to see is that the proxy eventually shuts down entirely.
			_, err = impersonationProxyViaSquidClient(nil).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			return err.Error() == serviceUnavailableViaSquidError
		}, 20*time.Second, 500*time.Millisecond)
	}

	// Check that the generated TLS cert Secret was deleted by the controller.
	require.Eventually(t, func() bool {
		caSecret, err = adminClient.CoreV1().Secrets(env.ConciergeNamespace).Get(ctx, impersonationProxyTLSSecretName, metav1.GetOptions{})
		return k8serrors.IsNotFound(err)
	}, 10*time.Second, 250*time.Millisecond)
}

func configMapForConfig(t *testing.T, config impersonator.Config) corev1.ConfigMap {
	configString, err := yaml.Marshal(config)
	require.NoError(t, err)
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: impersonationProxyConfigMapName,
		},
		Data: map[string]string{
			"config.yaml": string(configString),
		}}
	return configMap
}

func hasImpersonationProxyLoadBalancerService(ctx context.Context, client kubernetes.Interface, namespace string) (bool, error) {
	service, err := client.CoreV1().Services(namespace).Get(ctx, impersonationProxyLoadBalancerName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return service.Spec.Type == corev1.ServiceTypeLoadBalancer, nil
}

func getImpersonationProxyLoadBalancerIngress(ctx context.Context, client kubernetes.Interface, namespace string) (*corev1.LoadBalancerIngress, error) {
	service, err := client.CoreV1().Services(namespace).Get(ctx, impersonationProxyLoadBalancerName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	ingresses := service.Status.LoadBalancer.Ingress
	if len(ingresses) > 1 {
		return nil, fmt.Errorf("didn't expect multiple ingresses, but if it happens then maybe this test needs to be adjusted")
	}
	if len(ingresses) == 0 {
		return nil, nil
	}
	return &ingresses[0], nil
}