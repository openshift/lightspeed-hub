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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	spoke1Name = "e2e-spoke1"
	spoke2Name = "e2e-spoke2"

	credSecretName1 = "e2e-spoke1-creds"
	credSecretName2 = "e2e-spoke2-creds"

	managedNamespace  = "openshift-lightspeed-managed"
	agentSAName       = "lightspeed-agent"
	crbClusterReader  = "lightspeed-hub:cluster-reader"
	crbMonitoringView = "lightspeed-hub:cluster-monitoring-view"
)

var _ = Describe("Spoke lifecycle", Ordered, func() {
	BeforeAll(func() {
		By("Creating HubConfig in secret mode")
		hubConfig := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: hubv1alpha1.HubConfigSpec{
				ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeSecret,
			},
		}
		err := hubClient.Create(ctx, hubConfig)
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue(),
			"creating HubConfig: %v", err)

		By("Creating credential Secret for spoke1")
		spoke1Cred := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: credSecretName1, Namespace: operatorNamespace},
			Data:       map[string][]byte{"kubeconfig": spoke1InternalKubeconfig},
		}
		err = hubClient.Create(ctx, spoke1Cred)
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue(),
			"creating spoke1 credential Secret: %v", err)

		By("Creating credential Secret for spoke2")
		spoke2Cred := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: credSecretName2, Namespace: operatorNamespace},
			Data:       map[string][]byte{"kubeconfig": spoke2InternalKubeconfig},
		}
		err = hubClient.Create(ctx, spoke2Cred)
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue(),
			"creating spoke2 credential Secret: %v", err)

		By("Creating SpokeCluster for spoke1")
		sc1 := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spoke1Name},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: spoke1APIServer,
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name:      credSecretName1,
						Namespace: operatorNamespace,
					},
				},
			},
		}
		err = hubClient.Create(ctx, sc1)
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue(),
			"creating SpokeCluster spoke1: %v", err)

		By("Creating SpokeCluster for spoke2")
		sc2 := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spoke2Name},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: spoke2APIServer,
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name:      credSecretName2,
						Namespace: operatorNamespace,
					},
				},
			},
		}
		err = hubClient.Create(ctx, sc2)
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue(),
			"creating SpokeCluster spoke2: %v", err)
	})

	AfterAll(func() {
		By("AfterAll: cleaning up HubConfig, SpokeCluster CRs, and credential Secrets")
		_ = hubClient.Delete(ctx, &hubv1alpha1.SpokeCluster{ObjectMeta: metav1.ObjectMeta{Name: spoke1Name}})
		_ = hubClient.Delete(ctx, &hubv1alpha1.SpokeCluster{ObjectMeta: metav1.ObjectMeta{Name: spoke2Name}})
		_ = hubClient.Delete(ctx, &hubv1alpha1.HubConfig{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})
		_ = hubClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credSecretName1, Namespace: operatorNamespace}})
		_ = hubClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credSecretName2, Namespace: operatorNamespace}})
		// Restart any stopped kind containers so cluster teardown works cleanly
		if spoke1ContainerName != "" {
			startSpokeContainer(spoke1ContainerName)
		}
	})

	// -----------------------------------------------------------------------
	// Test 1: Registration — SpokeCluster with secret credential → Connected=True
	// -----------------------------------------------------------------------
	It("registers spoke1 and spoke2 with Connected=True", func() {
		By("Waiting for spoke1 Connected=True")
		waitForCondition(spoke1Name, "Connected")

		By("Verifying standing kubeconfig Secret exists on hub for spoke1")
		waitForSecret(fmt.Sprintf("spoke-kubeconfig-%s", spoke1Name))

		By("Verifying spoke1-side namespace created")
		var ns corev1.Namespace
		Eventually(func() bool {
			return spoke1Client.Get(ctx, client.ObjectKey{Name: managedNamespace}, &ns) == nil
		}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
			"namespace %s never appeared on spoke1", managedNamespace)

		By("Verifying spoke1-side lightspeed-agent SA created")
		var sa corev1.ServiceAccount
		Eventually(func() bool {
			return spoke1Client.Get(ctx, client.ObjectKey{Name: agentSAName, Namespace: managedNamespace}, &sa) == nil
		}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
			"ServiceAccount %s/%s never appeared on spoke1", managedNamespace, agentSAName)

		By("Verifying spoke1-side ClusterRoleBindings created")
		var crb rbacv1.ClusterRoleBinding
		Expect(spoke1Client.Get(ctx, client.ObjectKey{Name: crbClusterReader}, &crb)).To(Succeed())
		Expect(spoke1Client.Get(ctx, client.ObjectKey{Name: crbMonitoringView}, &crb)).To(Succeed())

		By("Waiting for spoke2 Connected=True")
		waitForCondition(spoke2Name, "Connected")
	})

	// -----------------------------------------------------------------------
	// Test 2: Provisioning + AdaptersReady (capability-aware)
	// -----------------------------------------------------------------------
	It("sets Provisioned=True and AdaptersReady according to cluster capability", func() {
		By("Waiting for spoke1 Provisioned=True")
		waitForCondition(spoke1Name, "Provisioned")

		By("Checking AdaptersReady based on cluster capability")
		if spokeHasAlertManager(spoke1Client) {
			// T2 path: real OCP with openshift-monitoring and AlertManager Route
			By("T2: waiting for AdaptersReady=True")
			waitForCondition(spoke1Name, "AdaptersReady")

			By("T2: verifying spoke-alert-credential Secret on hub")
			waitForSecret(fmt.Sprintf("spoke-alert-credential-%s", spoke1Name))
		} else {
			// T1 path: kind cluster — openshift-monitoring absent → ProvisionAdapter fails
			By("T1: waiting for AdaptersReady=False with reason AdaptersFailed")
			waitForConditionFalse(spoke1Name, "AdaptersReady")
			waitForConditionReason(spoke1Name, "AdaptersReady", "AdaptersFailed")
		}
	})

	// -----------------------------------------------------------------------
	// Test 3: Spoke unreachable → Connected=False, other spoke unaffected
	// -----------------------------------------------------------------------
	It("detects spoke1 as unreachable while spoke2 remains Connected=True", func() {
		if spoke1ContainerName == "" {
			Skip("MC_SPOKE_CONTAINER_NAMES not set — skipping spoke-unreachable test (not running under kind)")
		}

		By("Stopping spoke1 kind container to simulate network unreachability")
		stopSpokeContainer(spoke1ContainerName)

		By("Waiting for spoke1 Connected=False (health handler detects within ~15s interval)")
		waitForConditionFalse(spoke1Name, "Connected")

		By("Asserting spoke2 remains Connected=True")
		var sc2 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, client.ObjectKey{Name: spoke2Name}, &sc2)).To(Succeed())
		Expect(conditionTrue(&sc2, "Connected")).To(BeTrue(),
			"spoke2 Connected condition should still be True while spoke1 is down")
	})

	// -----------------------------------------------------------------------
	// Test 4: Decommission reachable spoke → spoke-side cleanup, no warning Event
	// -----------------------------------------------------------------------
	It("cleans up spoke2 (reachable) completely with no SpokeCleanupFailed event", func() {
		By("Deleting SpokeCluster for spoke2 (still reachable)")
		Expect(hubClient.Delete(ctx, &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spoke2Name},
		})).To(Succeed())

		By("Waiting for spoke2 SpokeCluster CR to be gone (finalizer released)")
		waitForSpokeNotFound(spoke2Name)

		By("Verifying spoke2 standing kubeconfig Secret is gone (auto-GC via owner ref)")
		var s corev1.Secret
		waitForNotFound(&s, client.ObjectKey{
			Name:      fmt.Sprintf("spoke-kubeconfig-%s", spoke2Name),
			Namespace: operatorNamespace,
		})

		By("Verifying spoke2-side managed namespace removed")
		Eventually(func() bool {
			var ns corev1.Namespace
			err := spoke2Client.Get(ctx, client.ObjectKey{Name: managedNamespace}, &ns)
			return apierrors.IsNotFound(err)
		}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
			"namespace %s still present on spoke2 after decommission", managedNamespace)

		By("Asserting no SpokeCleanupFailed event for any spoke at this point")
		var eventList corev1.EventList
		Expect(hubClient.List(ctx, &eventList, client.InNamespace(operatorNamespace))).To(Succeed())
		for _, e := range eventList.Items {
			Expect(e.Reason).NotTo(Equal("SpokeCleanupFailed"),
				"unexpected SpokeCleanupFailed event for %s: %s", e.InvolvedObject.Name, e.Message)
		}
	})

	// -----------------------------------------------------------------------
	// Test 5: Decommission reachable spoke1 → spoke-side cleanup, no warning Event
	// Restarts spoke1 if it was stopped in Test 3, verifies clean decommission,
	// then re-creates spoke1 for Test 6.
	// -----------------------------------------------------------------------
	It("cleans up spoke1 (reachable) with no SpokeCleanupFailed event", func() {
		// Restart spoke1 if stopped in Test 3 (kind only)
		if spoke1ContainerName != "" {
			By("Restarting spoke1 container")
			startSpokeContainer(spoke1ContainerName)
			By("Waiting for spoke1 Connected=True after restart")
			waitForCondition(spoke1Name, "Connected")
		}

		By("Deleting SpokeCluster for spoke1")
		Expect(hubClient.Delete(ctx, &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spoke1Name},
		})).To(Succeed())

		By("Waiting for spoke1 SpokeCluster CR to be gone")
		waitForSpokeNotFound(spoke1Name)

		By("Verifying spoke1-side managed namespace removed")
		Eventually(func() bool {
			var ns corev1.Namespace
			err := spoke1Client.Get(ctx, client.ObjectKey{Name: managedNamespace}, &ns)
			return apierrors.IsNotFound(err)
		}, defaultEventuallyTimeout, defaultEventuallyInterval).Should(BeTrue(),
			"namespace %s still present on spoke1", managedNamespace)

		By("Verifying standing kubeconfig Secret is gone")
		var s corev1.Secret
		waitForNotFound(&s, client.ObjectKey{
			Name:      fmt.Sprintf("spoke-kubeconfig-%s", spoke1Name),
			Namespace: operatorNamespace,
		})

		By("Asserting no SpokeCleanupFailed event for spoke1 in this clean deletion")
		var eventList corev1.EventList
		Expect(hubClient.List(ctx, &eventList, client.InNamespace(operatorNamespace))).To(Succeed())
		for _, e := range eventList.Items {
			if e.InvolvedObject.Name == spoke1Name {
				Expect(e.Reason).NotTo(Equal("SpokeCleanupFailed"),
					"unexpected SpokeCleanupFailed event for spoke1: %s", e.Message)
			}
		}

		By("Re-creating spoke1 for the unreachable-decommission test")
		sc1 := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spoke1Name},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: spoke1APIServer,
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name:      credSecretName1,
						Namespace: operatorNamespace,
					},
				},
			},
		}
		Expect(hubClient.Create(ctx, sc1)).To(Succeed())

		By("Waiting for spoke1 Connected=True (fresh registration)")
		waitForCondition(spoke1Name, "Connected")
	})

	// -----------------------------------------------------------------------
	// Test 6: Decommission unreachable spoke → SpokeCleanupFailed Event,
	//         hub-side cleanup proceeds, finalizer released (OLS-3948 AC2)
	// -----------------------------------------------------------------------
	It("emits SpokeCleanupFailed event and releases finalizer when spoke cleanup cannot parse kubeconfig", func() {
		By("Corrupting spoke1 standing kubeconfig (simulates unreachable cleanup path)")
		// The reconciler watches SpokeCluster and HubConfig only — NOT Secrets.
		// Corrupting the Secret does not trigger a reconcile that would restore it.
		// Deleting the SpokeCluster immediately triggers reconcileDelete, which reads
		// the corrupted bytes and sets spokeCleanupFailed=true, emitting the Event.
		GinkgoWriter.Println("Corrupting then immediately deleting — no reconcile window between the two ops")
		corruptStandingKubeconfig(spoke1Name)

		By("Deleting spoke1 SpokeCluster CR immediately after corruption")
		Expect(hubClient.Delete(ctx, &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spoke1Name},
		})).To(Succeed())

		By("Waiting for SpokeCleanupFailed warning Event")
		waitForSpokeCleanupFailedEvent(spoke1Name)

		By("Verifying hub-side cleanup proceeded: standing kubeconfig Secret gone")
		var s corev1.Secret
		waitForNotFound(&s, client.ObjectKey{
			Name:      fmt.Sprintf("spoke-kubeconfig-%s", spoke1Name),
			Namespace: operatorNamespace,
		})

		By("Verifying SpokeCluster CR is gone (finalizer released, deletion not blocked)")
		waitForSpokeNotFound(spoke1Name)
	})
})
