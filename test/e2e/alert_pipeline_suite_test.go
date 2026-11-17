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
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	alertAdapterDeployment = "lightspeed-hub-alerts-adapter"
	alertAdapterConfigMap  = "hub-alerts-adapter-config"
	alertAdapterConfigKey  = "config.yaml"
	requiredReceiver       = "critical"
	defaultAgentName       = "default"
	approvalPolicyName     = "cluster"
	agenticOLSConfigName   = "cluster"
)

// approvalStages are all ApprovalPolicy stages; the suite requires every one to be Automatic.
var approvalStages = []string{"Analysis", "Execution", "Verification", "Escalation"}

var (
	alertSpokeClient client.Client
	alertSpokeName   string
	skipLLMStages    bool
)

func TestAlertPipeline(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Alert Pipeline E2E Suite")
}

var _ = BeforeSuite(func() {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(hubv1alpha1.AddToScheme(scheme)).To(Succeed())

	hubClient = mustBuildClient(scheme, requireEnv("MC_HUB_KUBECONFIG"))
	alertSpokeClient = mustBuildClient(scheme, requireEnv("MC_SPOKE_KUBECONFIG"))
	alertSpokeName = requireEnv("MC_SPOKE_NAME")
	skipLLMStages = os.Getenv("SKIP_LLM_STAGES") == "true"

	By("Checking HubConfig and SpokeCluster readiness")
	adaptersReadySince := requireSpokeReady()

	By("Checking the multicluster alerts-adapter is running with an up-to-date config")
	podStart := requireAdapterPodStartedAfter(adaptersReadySince)
	requireAdapterConfig(podStart)

	By("Checking the agentic operator is installed and not suspended")
	requireAgenticReady()

	By("Ensuring ApprovalPolicy/cluster auto-approves every stage")
	ensureApprovalPolicyAutomatic()

	if skipLLMStages {
		By("SKIP_LLM_STAGES=true: skipping LLM configuration checks")
	} else {
		By("Checking Agent/default and its LLMProvider")
		requireDefaultAgent()
	}
})

// requireSpokeReady checks HubConfig exists and the SpokeCluster is fully onboarded.
// It returns when AdaptersReady last transitioned to True.
func requireSpokeReady() time.Time {
	var hc hubv1alpha1.HubConfig
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: "cluster"}, &hc)).To(Succeed(),
		"HubConfig/cluster must exist on the hub")

	var sc hubv1alpha1.SpokeCluster
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: alertSpokeName}, &sc)).To(Succeed(),
		"SpokeCluster/%s (MC_SPOKE_NAME) must exist on the hub", alertSpokeName)
	for _, condType := range []string{"Connected", "Provisioned", "AdaptersReady"} {
		Expect(meta.IsStatusConditionTrue(sc.Status.Conditions, condType)).To(BeTrue(),
			"SpokeCluster/%s must have %s=True; conditions: %+v", alertSpokeName, condType, sc.Status.Conditions)
	}
	return meta.FindStatusCondition(sc.Status.Conditions, "AdaptersReady").LastTransitionTime.Time
}

// requireAdapterPodStartedAfter checks the adapter Deployment is available with a single
// running pod that started after the spoke became AdaptersReady (the adapter discovers
// spokes only at startup). It returns the pod start time.
func requireAdapterPodStartedAfter(adaptersReadySince time.Time) time.Time {
	var deploy appsv1.Deployment
	Expect(hubClient.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: alertAdapterDeployment}, &deploy)).
		To(Succeed(), "Deployment %s/%s must exist (created by the hub for HubConfig)", operatorNamespace, alertAdapterDeployment)
	Expect(deploymentAvailable(&deploy)).To(BeTrue(),
		"Deployment %s/%s must be Available", operatorNamespace, alertAdapterDeployment)

	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	Expect(err).NotTo(HaveOccurred())
	var pods corev1.PodList
	Expect(hubClient.List(ctx, &pods, client.InNamespace(operatorNamespace),
		client.MatchingLabelsSelector{Selector: selector})).To(Succeed())

	var running []corev1.Pod
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && p.Status.StartTime != nil {
			running = append(running, p)
		}
	}
	Expect(running).To(HaveLen(1),
		"expected exactly one running %s pod (is a rollout in progress?)", alertAdapterDeployment)

	start := running[0].Status.StartTime.Time
	Expect(start.Before(adaptersReadySince)).To(BeFalse(),
		"adapter pod %s started at %s, before SpokeCluster/%s became AdaptersReady at %s; "+
			"restart the adapter: oc -n %s rollout restart deploy/%s",
		running[0].Name, start, alertSpokeName, adaptersReadySince, operatorNamespace, alertAdapterDeployment)
	return start
}

// requireAdapterConfig checks the adapter ConfigMap allows the Critical receiver, has no
// pre-run delay, and was last changed before the adapter pod started (config is read
// only at startup).
func requireAdapterConfig(podStart time.Time) {
	var cm corev1.ConfigMap
	Expect(hubClient.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: alertAdapterConfigMap}, &cm)).
		To(Succeed(), "ConfigMap %s/%s must exist", operatorNamespace, alertAdapterConfigMap)

	var cfg map[string]any
	Expect(yaml.Unmarshal([]byte(cm.Data[alertAdapterConfigKey]), &cfg)).To(Succeed(),
		"parsing %s in ConfigMap %s", alertAdapterConfigKey, alertAdapterConfigMap)

	receivers := allowedReceivers(cfg)
	Expect(receivers).To(ContainElement(requiredReceiver),
		"ConfigMap %s must set filtering.allowedReceivers to include Critical; got %v",
		alertAdapterConfigMap, receivers)

	delay, err := preRunDelay(cfg)
	Expect(err).NotTo(HaveOccurred(), "parsing preRunDelay in ConfigMap %s", alertAdapterConfigMap)
	Expect(delay).To(BeNumerically("<=", 0),
		"ConfigMap %s must leave preRunDelay unset or 0; got %s", alertAdapterConfigMap, delay)

	var lastChange time.Time
	for _, mf := range cm.ManagedFields {
		if mf.Time != nil && mf.Time.After(lastChange) {
			lastChange = mf.Time.Time
		}
	}
	Expect(podStart.Before(lastChange)).To(BeFalse(),
		"ConfigMap %s was changed at %s, after the adapter pod started at %s; "+
			"restart the adapter: oc -n %s rollout restart deploy/%s",
		alertAdapterConfigMap, lastChange, podStart, operatorNamespace, alertAdapterDeployment)
}

// allowedReceivers returns the lowercased receiver allowlist, preferring
// filtering.allowedReceivers over the legacy top-level key (same as the adapter).
func allowedReceivers(cfg map[string]any) []string {
	raw, found, _ := unstructured.NestedStringSlice(cfg, "filtering", "allowedReceivers")
	if !found {
		raw, _, _ = unstructured.NestedStringSlice(cfg, "allowedReceivers")
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, strings.ToLower(strings.TrimSpace(r)))
	}
	return out
}

// preRunDelay returns the configured preRunDelay, or 0 when unset.
func preRunDelay(cfg map[string]any) (time.Duration, error) {
	v, ok := cfg["preRunDelay"]
	if !ok || v == nil {
		return 0, nil
	}
	switch d := v.(type) {
	case string:
		return time.ParseDuration(d)
	case float64:
		return time.Duration(d), nil
	case int64:
		return time.Duration(d), nil
	default:
		return 0, nil
	}
}

// requireAgenticReady checks the AgenticRun CRD is served and AgenticOLSConfig (if any)
// is not suspended.
func requireAgenticReady() {
	runs := newAgenticList("AgenticRunList")
	Expect(hubClient.List(ctx, runs, client.InNamespace(operatorNamespace), client.Limit(1))).To(Succeed(),
		"listing AgenticRuns failed; is lightspeed-agentic-operator installed on the hub?")

	cfg := newAgentic("AgenticOLSConfig")
	err := hubClient.Get(ctx, client.ObjectKey{Name: agenticOLSConfigName}, cfg)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred(), "getting AgenticOLSConfig/%s", agenticOLSConfigName)
	suspended, _, _ := unstructured.NestedBool(cfg.Object, "spec", "suspended")
	Expect(suspended).To(BeFalse(), "AgenticOLSConfig/%s must not be suspended", agenticOLSConfigName)
}

// ensureApprovalPolicyAutomatic creates ApprovalPolicy/cluster with every stage Automatic
// when absent, then requires every stage to be Automatic. It never modifies or deletes an
// existing policy.
func ensureApprovalPolicyAutomatic() {
	policy := newAgentic("ApprovalPolicy")
	policy.SetName(approvalPolicyName)
	stages := make([]any, 0, len(approvalStages))
	for _, s := range approvalStages {
		stages = append(stages, map[string]any{"name": s, "approval": "Automatic"})
	}
	Expect(unstructured.SetNestedSlice(policy.Object, stages, "spec", "stages")).To(Succeed())
	err := hubClient.Create(ctx, policy)
	Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue(),
		"creating ApprovalPolicy/%s: %v", approvalPolicyName, err)

	existing := newAgentic("ApprovalPolicy")
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: approvalPolicyName}, existing)).To(Succeed())
	got, _, _ := unstructured.NestedSlice(existing.Object, "spec", "stages")
	modes := map[string]string{}
	for _, s := range got {
		if m, ok := s.(map[string]any); ok {
			name, _ := m["name"].(string)
			mode, _ := m["approval"].(string)
			modes[name] = mode
		}
	}
	for _, s := range approvalStages {
		Expect(modes[s]).To(Equal("Automatic"),
			"ApprovalPolicy/%s must set stage %s to Automatic (got %q)", approvalPolicyName, s, modes[s])
	}
}

// requireDefaultAgent checks Agent/default has a model and an existing LLMProvider.
func requireDefaultAgent() {
	agent := newAgentic("Agent")
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: defaultAgentName}, agent)).To(Succeed(),
		"Agent/%s must exist (the alerts-adapter assigns it to every step)", defaultAgentName)
	model, _, _ := unstructured.NestedString(agent.Object, "spec", "model")
	providerName, _, _ := unstructured.NestedString(agent.Object, "spec", "llmProvider", "name")
	Expect(model).NotTo(BeEmpty(), "Agent/%s must set spec.model", defaultAgentName)
	Expect(providerName).NotTo(BeEmpty(), "Agent/%s must set spec.llmProvider.name", defaultAgentName)

	provider := newAgentic("LLMProvider")
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: providerName}, provider)).To(Succeed(),
		"LLMProvider/%s referenced by Agent/%s must exist", providerName, defaultAgentName)
}

func deploymentAvailable(d *appsv1.Deployment) bool {
	return slices.ContainsFunc(d.Status.Conditions, func(c appsv1.DeploymentCondition) bool {
		return c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue
	})
}
