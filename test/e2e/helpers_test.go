//go:build mc_e2e || mc_product_e2e

/*
Copyright 2026 Red Hat, Inc..

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	defaultEventuallyTimeout  = 2 * time.Minute
	defaultEventuallyInterval = 5 * time.Second
)

// waitForCondition polls until the named condition on the SpokeCluster reaches status=True.
// An optional timeout overrides defaultEventuallyTimeout (e.g. for health-handler-driven waits).
func waitForCondition(spokeName, condType string, timeout ...time.Duration) {
	to := defaultEventuallyTimeout
	if len(timeout) > 0 {
		to = timeout[0]
	}
	GinkgoWriter.Printf("Waiting for SpokeCluster %s condition %s=True (timeout %s)\n", spokeName, condType, to)
	Eventually(func() bool {
		var sc hubv1alpha1.SpokeCluster
		if err := hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc); err != nil {
			return false
		}
		c := meta.FindStatusCondition(sc.Status.Conditions, condType)
		return c != nil && c.Status == metav1.ConditionTrue
	}, to, defaultEventuallyInterval).Should(BeTrue(),
		"SpokeCluster %s condition %s never reached True", spokeName, condType)
}

// waitForConditionFalse polls until the named condition on the SpokeCluster reaches status=False.
// An optional timeout overrides defaultEventuallyTimeout (e.g. for health-handler-driven waits).
func waitForConditionFalse(spokeName, condType string, timeout ...time.Duration) {
	to := defaultEventuallyTimeout
	if len(timeout) > 0 {
		to = timeout[0]
	}
	GinkgoWriter.Printf("Waiting for SpokeCluster %s condition %s=False (timeout %s)\n", spokeName, condType, to)
	Eventually(func() bool {
		var sc hubv1alpha1.SpokeCluster
		if err := hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc); err != nil {
			return false
		}
		c := meta.FindStatusCondition(sc.Status.Conditions, condType)
		return c != nil && c.Status == metav1.ConditionFalse
	}, to, defaultEventuallyInterval).Should(BeTrue(),
		"SpokeCluster %s condition %s never reached False", spokeName, condType)
}

// waitForConditionReason polls until the named condition has the given reason.
func waitForConditionReason(spokeName, condType, reason string) {
	GinkgoWriter.Printf("Waiting for SpokeCluster %s condition %s reason=%s\n", spokeName, condType, reason)
	Eventually(func() bool {
		var sc hubv1alpha1.SpokeCluster
		if err := hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc); err != nil {
			return false
		}
		c := meta.FindStatusCondition(sc.Status.Conditions, condType)
		return c != nil && c.Reason == reason
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
		"SpokeCluster %s condition %s never got reason %s", spokeName, condType, reason)
}

// waitForSecret polls until the Secret exists in operatorNamespace.
func waitForSecret(name string) {
	Eventually(func() bool {
		var s corev1.Secret
		return hubClient.Get(ctx, client.ObjectKey{Name: name, Namespace: operatorNamespace}, &s) == nil
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
		"Secret %s/%s never appeared", operatorNamespace, name)
}

// waitForNotFound polls until the object is gone (returns NotFound).
func waitForNotFound(obj client.Object, key client.ObjectKey) {
	Eventually(func() bool {
		err := hubClient.Get(ctx, key, obj)
		return apierrors.IsNotFound(err)
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
		"object %s/%s never disappeared", key.Namespace, key.Name)
}

// waitForSpokeNotFound polls until the cluster-scoped SpokeCluster is gone.
func waitForSpokeNotFound(spokeName string) {
	Eventually(func() bool {
		var sc hubv1alpha1.SpokeCluster
		err := hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc)
		return apierrors.IsNotFound(err)
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
		"SpokeCluster %s never disappeared", spokeName)
}

// spokeHasAlertManager returns true if the spoke has the openshift-monitoring namespace,
// indicating a real OCP cluster (T2 path). On kind (T1), it is absent.
func spokeHasAlertManager(spokeClient client.Client) bool {
	var ns corev1.Namespace
	err := spokeClient.Get(ctx, client.ObjectKey{Name: "openshift-monitoring"}, &ns)
	return err == nil
}

// makeCredentialUnreachable simulates a spoke being unreachable:
//  1. Replaces the credential Secret with garbage YAML so the reconciler fails at
//     GetRESTConfig and returns early — ensureStandingKubeconfig is never called,
//     so the standing kubeconfig is not restored by the reconciler.
//  2. Redirects the standing kubeconfig to 192.0.2.1:6443 (TEST-NET, RFC 5737)
//     so the health handler detects unreachability within ~15s → Connected=False.
func makeCredentialUnreachable(ctx context.Context, spokeName, credSecretName string) {
	const unreachableServer = "https://192.0.2.1:6443"

	// Step 1: put garbage in the credential Secret to stop the reconciler from restoring
	var credSecret corev1.Secret
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: credSecretName, Namespace: operatorNamespace}, &credSecret)).To(Succeed())
	credSecret.Data["kubeconfig"] = []byte("not-valid-kubeconfig {{{{ garbage")
	Expect(hubClient.Update(ctx, &credSecret)).To(Succeed())

	// Step 2: redirect the standing kubeconfig to an unreachable server so the
	// health handler (which reads the standing kubeconfig directly) fails connectivity
	var standingSecret corev1.Secret
	standingKey := client.ObjectKey{
		Name:      fmt.Sprintf("spoke-kubeconfig-%s", spokeName),
		Namespace: operatorNamespace,
	}
	Expect(hubClient.Get(ctx, standingKey, &standingSecret)).To(Succeed())
	cfg, err := clientcmd.Load(standingSecret.Data["kubeconfig"])
	Expect(err).NotTo(HaveOccurred())
	for name := range cfg.Clusters {
		cfg.Clusters[name].Server = unreachableServer
		cfg.Clusters[name].InsecureSkipTLSVerify = true
		cfg.Clusters[name].CertificateAuthorityData = nil
	}
	modified, err := clientcmd.Write(*cfg)
	Expect(err).NotTo(HaveOccurred())
	standingSecret.Data["kubeconfig"] = modified
	Expect(hubClient.Update(ctx, &standingSecret)).To(Succeed())

	GinkgoWriter.Printf("Credential for spoke %s set to garbage (blocks reconciler from restoring); "+
		"standing kubeconfig redirected to %s (health handler detects within ~15s)\n", spokeName, unreachableServer)
}

// restoreCredential restores the credential Secret to the original kubeconfig bytes
// and triggers a reconcile (via SpokeCluster annotation) so the reconciler rebuilds
// the standing kubeconfig and connectivity is re-established → Connected=True.
// A brief sleep before the annotation allows the informer cache to propagate.
func restoreCredential(ctx context.Context, spokeName, credSecretName string, original []byte) {
	secretKey := client.ObjectKey{Name: credSecretName, Namespace: operatorNamespace}
	var secret corev1.Secret
	Expect(hubClient.Get(ctx, secretKey, &secret)).To(Succeed())
	secret.Data["kubeconfig"] = original
	Expect(hubClient.Update(ctx, &secret)).To(Succeed())

	// Allow informer cache to propagate before triggering the reconcile so the
	// reconciler reads the restored kubeconfig and rebuilds a valid standing kubeconfig.
	time.Sleep(3 * time.Second)
	touchSpokeCluster(ctx, spokeName, "")
	GinkgoWriter.Printf("Credential for spoke %s restored; reconcile triggered\n", spokeName)
}

// touchSpokeCluster adds/updates an annotation on the SpokeCluster to trigger a
// controller-runtime watch event and force a reconcile.
func touchSpokeCluster(ctx context.Context, spokeName, value string) {
	var sc hubv1alpha1.SpokeCluster
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc)).To(Succeed())
	if sc.Annotations == nil {
		sc.Annotations = make(map[string]string)
	}
	sc.Annotations["e2e.test/trigger"] = value
	Expect(hubClient.Update(ctx, &sc)).To(Succeed())
}

// corruptStandingKubeconfig replaces the standing kubeconfig Secret's kubeconfig bytes
// with unparseable content so cleanupSpokeResources cannot parse it, triggering
// spokeCleanupFailed=true and the SpokeCleanupFailed Event.
// The reconciler does NOT watch Secrets, so this is not immediately reversed.
// Delete the SpokeCluster immediately after calling this.
func corruptStandingKubeconfig(spokeName string) {
	secretKey := client.ObjectKey{
		Name:      fmt.Sprintf("spoke-kubeconfig-%s", spokeName),
		Namespace: operatorNamespace,
	}
	var secret corev1.Secret
	Expect(hubClient.Get(ctx, secretKey, &secret)).To(Succeed(),
		"reading standing kubeconfig Secret for %s", spokeName)
	secret.Data["kubeconfig"] = []byte("not-valid-kubeconfig {{{{ garbage")
	Expect(hubClient.Update(ctx, &secret)).To(Succeed(),
		"corrupting standing kubeconfig Secret for %s", spokeName)
	GinkgoWriter.Printf("Corrupted standing kubeconfig for spoke %s\n", spokeName)
}

// waitForSpokeCleanupFailedEvent polls until a SpokeCleanupFailed warning Event exists
// for the named spoke. SpokeCluster is cluster-scoped so its Events land in the
// "default" namespace regardless of where the operator runs.
func waitForSpokeCleanupFailedEvent(spokeName string) {
	GinkgoWriter.Printf("Waiting for SpokeCleanupFailed event for spoke %s (in 'default' ns)\n", spokeName)
	Eventually(func() bool {
		var eventList corev1.EventList
		// Events for cluster-scoped objects are created in the "default" namespace
		if err := hubClient.List(ctx, &eventList, client.InNamespace("default")); err != nil {
			return false
		}
		for _, e := range eventList.Items {
			if e.Reason == "SpokeCleanupFailed" && e.InvolvedObject.Name == spokeName {
				return true
			}
		}
		return false
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
		"SpokeCleanupFailed Event never appeared for spoke %s", spokeName)
}

// conditionTrue returns true if the SpokeCluster has the named condition with status True.
func conditionTrue(sc *hubv1alpha1.SpokeCluster, condType string) bool {
	c := meta.FindStatusCondition(sc.Status.Conditions, condType)
	return c != nil && c.Status == metav1.ConditionTrue
}

// conditionFalse returns true if the SpokeCluster has the named condition with status False.
func conditionFalse(sc *hubv1alpha1.SpokeCluster, condType string) bool {
	c := meta.FindStatusCondition(sc.Status.Conditions, condType)
	return c != nil && c.Status == metav1.ConditionFalse
}
