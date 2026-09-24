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
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	defaultEventuallyTimeout  = 2 * time.Minute
	defaultEventuallyInterval = 5 * time.Second
)

// waitForCondition polls until the named condition on the SpokeCluster reaches status=True.
func waitForCondition(spokeName, condType string) {
	GinkgoWriter.Printf("Waiting for SpokeCluster %s condition %s=True\n", spokeName, condType)
	Eventually(func() bool {
		var sc hubv1alpha1.SpokeCluster
		if err := hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc); err != nil {
			return false
		}
		c := meta.FindStatusCondition(sc.Status.Conditions, condType)
		return c != nil && c.Status == metav1.ConditionTrue
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
		"SpokeCluster %s condition %s never reached True", spokeName, condType)
}

// waitForConditionFalse polls until the named condition on the SpokeCluster reaches status=False.
func waitForConditionFalse(spokeName, condType string) {
	GinkgoWriter.Printf("Waiting for SpokeCluster %s condition %s=False\n", spokeName, condType)
	Eventually(func() bool {
		var sc hubv1alpha1.SpokeCluster
		if err := hubClient.Get(ctx, client.ObjectKey{Name: spokeName}, &sc); err != nil {
			return false
		}
		c := meta.FindStatusCondition(sc.Status.Conditions, condType)
		return c != nil && c.Status == metav1.ConditionFalse
	}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
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

// stopSpokeContainer stops the kind cluster container by name.
// Skips the calling test if containerName is empty (T2: no container names set).
func stopSpokeContainer(containerName string) {
	if containerName == "" {
		Skip("MC_SPOKE_CONTAINER_NAMES not set — skipping spoke-unreachable test (not running under kind)")
	}
	GinkgoWriter.Printf("Stopping kind container %s\n", containerName)
	out, err := exec.Command("docker", "stop", containerName).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "docker stop %s: %s", containerName, out)
}

// startSpokeContainer restarts a previously stopped kind cluster container.
func startSpokeContainer(containerName string) {
	if containerName == "" {
		return
	}
	GinkgoWriter.Printf("Starting kind container %s\n", containerName)
	out, err := exec.Command("docker", "start", containerName).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "docker start %s: %s", containerName, out)
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
// for the named spoke in the operator namespace.
func waitForSpokeCleanupFailedEvent(spokeName string) {
	GinkgoWriter.Printf("Waiting for SpokeCleanupFailed event for spoke %s\n", spokeName)
	Eventually(func() bool {
		var eventList corev1.EventList
		if err := hubClient.List(ctx, &eventList, client.InNamespace(operatorNamespace)); err != nil {
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
