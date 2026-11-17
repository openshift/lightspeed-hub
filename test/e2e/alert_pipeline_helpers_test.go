//go:build alert_pipeline_e2e

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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	alertName          = "LightspeedE2EDeploymentUnavailable"
	alertNameLabel     = "lightspeede2edeploymentunavailable" // adapter lowercases the alert name
	alertNamespaceBase = "ls-alert-e2e-"
	ownedLabel         = "lightspeed.openshift.io/alert-pipeline-e2e"
	appName            = "e2e-app"
	appConfigMapName   = "e2e-app-config"
	appConfigKey       = "proof"
	appImage           = "registry.access.redhat.com/ubi9/ubi-minimal:latest"

	labelSpokeCluster = "hub.openshift.io/spoke-cluster"
	labelSource       = "agentic.openshift.io/source"
	labelAlertName    = "agentic.openshift.io/alert-name"
	labelSeverity     = "agentic.openshift.io/alert-severity"
	labelAlertGroupID = "agentic.openshift.io/alert-group-id"

	// Rule creation to AgenticRun measured at ~2m40s on OCP (scrape + rule evaluation +
	// for: 1m + AlertManager group_wait + adapter poll).
	agenticRunCreatedTimeout = 5 * time.Minute
	// Matches the agentic operator default analysis/execution step timeout (600s).
	agenticStageTimeout = 10 * time.Minute
	cleanupTimeout      = 3 * time.Minute
	pollInterval        = 5 * time.Second

	// faultResolveTimeout bounds waiting for the Deployment to become Available after cleanup
	// creates the missing ConfigMap.
	faultResolveTimeout = 2 * time.Minute
	// alertClearSettle is how long after the fault is fixed the alert may stay active in
	// AlertManager (measured ~2.5m: scrape + rule evaluation + resolved notification).
	alertClearSettle = 4 * time.Minute
	// alertExpirySettle is how long AlertManager keeps an alert active after its rule is
	// deleted without a resolved notification (4x the 1m resend interval) plus one poll.
	alertExpirySettle = 5 * time.Minute
)

var agenticGV = schema.GroupVersion{Group: "agentic.openshift.io", Version: "v1alpha1"}

// newAgentic returns an empty Unstructured of the given agentic.openshift.io kind.
func newAgentic(kind string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(agenticGV.WithKind(kind))
	return u
}

// newAgenticList returns an empty UnstructuredList of the given agentic.openshift.io list kind.
func newAgenticList(kind string) *unstructured.UnstructuredList {
	u := &unstructured.UnstructuredList{}
	u.SetGroupVersionKind(agenticGV.WithKind(kind))
	return u
}

// alertFixture is the per-run fault created on the spoke.
type alertFixture struct {
	id              string
	namespace       string
	ruleCreated     bool
	appGeneration   int64
	agenticRunName  string
	diagnosticsDone bool
}

func newAlertFixture() *alertFixture {
	raw := make([]byte, 4)
	_, err := rand.Read(raw)
	Expect(err).NotTo(HaveOccurred())
	id := hex.EncodeToString(raw)
	return &alertFixture{id: id, namespace: alertNamespaceBase + id}
}

func (f *alertFixture) namespaceObject() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: f.namespace,
		Labels: map[string]string{
			"openshift.io/cluster-monitoring": "true",
			ownedLabel:                        f.id,
		},
	}}
}

// deployment mounts a ConfigMap that does not exist, so its pod never starts.
func (f *alertFixture) deployment() *appsv1.Deployment {
	labels := map[string]string{"app": appName}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: f.namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:    "app",
						Image:   appImage,
						Command: []string{"sleep", "infinity"},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/e2e-app", ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: appConfigMapName},
						}},
					}},
				},
			},
		},
	}
}

// prometheusRule fires while the Deployment has no available replicas.
func (f *alertFixture) prometheusRule() *unstructured.Unstructured {
	rule := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "PrometheusRule",
		"metadata": map[string]any{
			"name":      appName + "-alerts",
			"namespace": f.namespace,
			"labels":    map[string]any{ownedLabel: f.id},
		},
		"spec": map[string]any{
			"groups": []any{map[string]any{
				"name": "lightspeed-alert-pipeline-e2e",
				"rules": []any{map[string]any{
					"alert": alertName,
					"expr": fmt.Sprintf(`kube_deployment_status_replicas_available{namespace=%q,deployment=%q} < 1`,
						f.namespace, appName),
					"for":    "1m",
					"labels": map[string]any{"severity": "critical"},
					"annotations": map[string]any{
						"description": fmt.Sprintf("Deployment %s in namespace %s has no available replicas. "+
							"It requires ConfigMap %s with key %s=%s.", appName, f.namespace, appConfigMapName, appConfigKey, f.id),
					},
				}},
			}},
		},
	}}
	return rule
}

// listFixtureRuns returns the AgenticRuns the adapter created for this fixture's alert.
// The alert name label is shared across runs, so runs are matched on spec.targetCluster
// and the per-run namespace rendered into spec.request.
func (f *alertFixture) listFixtureRuns() []unstructured.Unstructured {
	runs := newAgenticList("AgenticRunList")
	Expect(hubClient.List(ctx, runs, client.InNamespace(operatorNamespace), client.MatchingLabels{
		labelAlertName: alertNameLabel,
	})).To(Succeed())
	var out []unstructured.Unstructured
	for _, r := range runs.Items {
		targetCluster, _, _ := unstructured.NestedString(r.Object, "spec", "targetCluster")
		request, _, _ := unstructured.NestedString(r.Object, "spec", "request")
		if targetCluster == alertSpokeName && strings.Contains(request, f.namespace) {
			out = append(out, r)
		}
	}
	return out
}

// spokeTargetID mirrors the alerts-adapter's SpokeTargetID
// (lightspeed-agentic-alerts-adapter internal/agenticrun/client.go): the value of the
// hub.openshift.io/spoke-cluster label is "spoke-<name>", truncated with a hash suffix
// when longer than 63 characters.
func spokeTargetID(name string) string {
	const prefix, hashLen, maxLen = "spoke-", 12, 63
	if len(prefix)+len(name) <= maxLen {
		return prefix + name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:hashLen]
	return prefix + name[:maxLen-len(prefix)-len(suffix)-1] + "-" + suffix
}

func (f *alertFixture) getRun() *unstructured.Unstructured {
	run := newAgentic("AgenticRun")
	Expect(hubClient.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: f.agenticRunName}, run)).To(Succeed())
	return run
}

// runCondition returns the status and "reason: message" of a condition on an AgenticRun.
func runCondition(run *unstructured.Unstructured, condType string) (status, detail string) {
	conds, _, _ := unstructured.NestedSlice(run.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != condType {
			continue
		}
		status, _ = m["status"].(string)
		reason, _ := m["reason"].(string)
		message, _ := m["message"].(string)
		return status, reason + ": " + message
	}
	return "", ""
}

// terminalRunConditions end a run without success; any of them being True fails the wait.
var terminalRunConditions = []string{"Denied", "Escalated", "EmergencyStopped"}

// waitForRunCondition waits for condType=True on the fixture's AgenticRun, stopping
// immediately if the step fails or the run ends without success.
func (f *alertFixture) waitForRunCondition(condType string) {
	Eventually(func(g Gomega) {
		run := f.getRun()
		for _, t := range terminalRunConditions {
			if status, detail := runCondition(run, t); status == string(metav1.ConditionTrue) {
				StopTrying(fmt.Sprintf("AgenticRun %s ended with %s=True (%s) while waiting for %s",
					f.agenticRunName, t, detail, condType)).Now()
			}
		}
		status, detail := runCondition(run, condType)
		if status == string(metav1.ConditionFalse) {
			StopTrying(fmt.Sprintf("AgenticRun %s has %s=False (%s)", f.agenticRunName, condType, detail)).Now()
		}
		g.Expect(status).To(Equal(string(metav1.ConditionTrue)), "%s is %q (%s)", condType, status, detail)
	}, agenticStageTimeout, pollInterval).Should(Succeed(), "waiting for %s=True on AgenticRun %s", condType, f.agenticRunName)
}

// cleanup removes the fixture without leaving AgenticRuns behind. Deleting the namespace
// alone does not clear the alert: AlertManager keeps it active for minutes, and the adapter
// recreates a run as soon as the previous one is deleted. Existing fixture runs block
// duplicates (active-run and postRunDelay dedup), so they are kept until the alert clears.
func (f *alertFixture) cleanup() {
	if !f.ruleCreated {
		f.deleteRuns()
		f.deleteNamespace()
		return
	}

	if since, ok := f.resolveFault(); ok {
		settleUntil := since.Add(alertClearSettle)
		if wait := time.Until(settleUntil); wait > 0 {
			By(fmt.Sprintf("Waiting %s for the resolved alert to clear before deleting AgenticRuns", wait.Round(time.Second)))
			time.Sleep(wait)
		}
		f.deleteRuns()
		f.deleteNamespace()
		return
	}

	By(fmt.Sprintf("Fault could not be resolved; deleting the namespace and waiting %s for the alert to expire", alertExpirySettle))
	f.deleteNamespace()
	time.Sleep(alertExpirySettle)
	f.deleteRuns()
}

// resolveFault creates the missing ConfigMap (if the agent did not) and waits for the
// Deployment to become Available, so the alert resolves through Prometheus. It returns
// when the Deployment became Available, or false if it did not.
func (f *alertFixture) resolveFault() (time.Time, bool) {
	By("Resolving the fault so the alert clears")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: appConfigMapName, Namespace: f.namespace},
		Data:       map[string]string{appConfigKey: f.id},
	}
	if err := alertSpokeClient.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		GinkgoWriter.Printf("creating ConfigMap %s/%s: %v\n", f.namespace, appConfigMapName, err)
		return time.Time{}, false
	}

	var since time.Time
	err := wait.PollUntilContextTimeout(ctx, pollInterval, faultResolveTimeout, true, func(context.Context) (bool, error) {
		var deploy appsv1.Deployment
		if err := alertSpokeClient.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: appName}, &deploy); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		for _, c := range deploy.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
				since = c.LastTransitionTime.Time
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		GinkgoWriter.Printf("Deployment %s/%s did not become Available: %v\n", f.namespace, appName, err)
		return time.Time{}, false
	}
	return since, true
}

// deleteRuns deletes every AgenticRun created for this fixture and waits for them to be
// gone. Finalizers are never stripped: a stuck run fails cleanup visibly.
func (f *alertFixture) deleteRuns() {
	runs := f.listFixtureRuns()
	for i := range runs {
		GinkgoWriter.Printf("Deleting AgenticRun %s\n", runs[i].GetName())
		Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &runs[i]))).To(Succeed())
	}
	Eventually(func() []string {
		var names []string
		for _, r := range f.listFixtureRuns() {
			names = append(names, r.GetName())
		}
		return names
	}, cleanupTimeout, pollInterval).Should(BeEmpty(),
		"AgenticRuns for %s still present after delete (stuck finalizer?)", f.namespace)
}

// deleteNamespace deletes the spoke namespace (and with it the PrometheusRule) and waits for it to be gone.
func (f *alertFixture) deleteNamespace() {
	ns := f.namespaceObject()
	Expect(client.IgnoreNotFound(alertSpokeClient.Delete(ctx, ns))).To(Succeed())
	Eventually(func() bool {
		err := alertSpokeClient.Get(ctx, client.ObjectKeyFromObject(ns), &corev1.Namespace{})
		return apierrors.IsNotFound(err)
	}, cleanupTimeout, pollInterval).Should(BeTrue(), "spoke namespace %s still present after delete", f.namespace)
}

// dumpDiagnostics logs the AgenticRun status, its step results and spoke namespace events.
func (f *alertFixture) dumpDiagnostics() {
	if f.diagnosticsDone {
		return
	}
	f.diagnosticsDone = true
	GinkgoWriter.Println("===== alert pipeline diagnostics =====")

	if f.agenticRunName != "" {
		run := newAgentic("AgenticRun")
		if err := hubClient.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: f.agenticRunName}, run); err != nil {
			GinkgoWriter.Printf("getting AgenticRun %s: %v\n", f.agenticRunName, err)
		} else {
			printYAML("AgenticRun "+f.agenticRunName+" status", run.Object["status"])
			for step, kind := range map[string]string{
				"analysis": "AnalysisResult", "execution": "ExecutionResult", "verification": "VerificationResult",
			} {
				refs, _, _ := unstructured.NestedSlice(run.Object, "status", "steps", step, "results")
				for _, ref := range refs {
					m, _ := ref.(map[string]any)
					name, _ := m["name"].(string)
					if name == "" {
						continue
					}
					result := newAgentic(kind)
					if err := hubClient.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: name}, result); err != nil {
						GinkgoWriter.Printf("getting %s %s: %v\n", kind, name, err)
						continue
					}
					printYAML(kind+" "+name, map[string]any{"spec": result.Object["spec"], "status": result.Object["status"]})
				}
			}
		}
	}

	var events corev1.EventList
	if err := alertSpokeClient.List(ctx, &events, client.InNamespace(f.namespace)); err != nil {
		GinkgoWriter.Printf("listing events in spoke namespace %s: %v\n", f.namespace, err)
	} else {
		GinkgoWriter.Printf("--- spoke events in %s ---\n", f.namespace)
		for _, e := range events.Items {
			GinkgoWriter.Printf("%s %s %s/%s: %s\n", e.LastTimestamp.Format(time.RFC3339), e.Reason,
				e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Message)
		}
	}
}

func printYAML(title string, v any) {
	out, err := yaml.Marshal(v)
	if err != nil {
		GinkgoWriter.Printf("--- %s: marshal error: %v\n", title, err)
		return
	}
	GinkgoWriter.Printf("--- %s ---\n%s\n", title, out)
}
