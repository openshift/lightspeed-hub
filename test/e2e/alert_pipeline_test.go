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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Full alert pipeline: spoke PrometheusRule -> spoke AlertManager -> hub multicluster
// alerts-adapter -> AgenticRun on the hub -> Analysis/Execution/Verification on the spoke.
// See .ai/spec/what/multicluster-testing.md, "Alert pipeline E2E".
var _ = Describe("Alert pipeline", Ordered, func() {
	var f *alertFixture

	BeforeAll(func() {
		f = newAlertFixture()
		GinkgoWriter.Printf("Fixture id %s, spoke namespace %s\n", f.id, f.namespace)

		By("Creating the per-run namespace on the spoke")
		Expect(alertSpokeClient.Create(ctx, f.namespaceObject())).To(Succeed())
		DeferCleanup(func() {
			By("Cleaning up the fixture: resolve the fault, then delete AgenticRuns and the namespace")
			f.cleanup()
		})

		By("Creating a Deployment whose required ConfigMap is missing")
		deploy := f.deployment()
		Expect(alertSpokeClient.Create(ctx, deploy)).To(Succeed())
		f.appGeneration = deploy.Generation

		By("Creating the PrometheusRule that fires while the Deployment is unavailable")
		Expect(alertSpokeClient.Create(ctx, f.prometheusRule())).To(Succeed())
		f.ruleCreated = true
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() && f != nil {
			f.dumpDiagnostics()
		}
	})

	It("Phase 1: the adapter creates an AgenticRun for the spoke alert", func() {
		var runs []unstructured.Unstructured
		Eventually(func() []unstructured.Unstructured {
			runs = f.listFixtureRuns()
			return runs
		}, agenticRunCreatedTimeout, pollInterval).Should(HaveLen(1),
			"expected exactly one AgenticRun for alert %s in spoke namespace %s", alertName, f.namespace)

		run := runs[0]
		f.agenticRunName = run.GetName()
		GinkgoWriter.Printf("AgenticRun %s/%s created\n", operatorNamespace, f.agenticRunName)

		targetCluster, _, _ := unstructured.NestedString(run.Object, "spec", "targetCluster")
		Expect(targetCluster).To(Equal(alertSpokeName), "spec.targetCluster")

		labels := run.GetLabels()
		Expect(labels).To(HaveKeyWithValue(labelSpokeCluster, spokeTargetID(alertSpokeName)))
		Expect(labels).To(HaveKeyWithValue(labelSource, "alertmanager"))
		Expect(labels).To(HaveKeyWithValue(labelSeverity, "critical"))
		Expect(labels).To(HaveKey(labelAlertGroupID))

		request, _, _ := unstructured.NestedString(run.Object, "spec", "request")
		Expect(request).To(ContainSubstring(alertName), "spec.request")
		Expect(request).To(ContainSubstring(appConfigKey+"="+f.id), "spec.request")

		for _, step := range []string{"analysis", "execution", "verification"} {
			agent, _, _ := unstructured.NestedString(run.Object, "spec", step, "agent")
			Expect(agent).To(Equal(defaultAgentName), "spec.%s.agent", step)
		}
	})

	It("Phase 2: the agentic stages fix the fault", func() {
		if skipLLMStages {
			Skip("SKIP_LLM_STAGES=true")
		}

		for _, cond := range []string{"Analyzed", "Executed", "Verified"} {
			By("Waiting for " + cond + "=True")
			f.waitForRunCondition(cond)
		}

		By("Verifying the fix on the spoke independently of the run's conditions")
		var cm corev1.ConfigMap
		Expect(alertSpokeClient.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: appConfigMapName}, &cm)).
			To(Succeed(), "ConfigMap %s must have been created by the execution step", appConfigMapName)
		Expect(cm.Data).To(HaveKeyWithValue(appConfigKey, f.id))

		var deploy appsv1.Deployment
		Eventually(func(g Gomega) {
			g.Expect(alertSpokeClient.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: appName}, &deploy)).To(Succeed())
			g.Expect(deploymentAvailable(&deploy)).To(BeTrue(), "Deployment %s Available", appName)
		}, time.Minute, pollInterval).Should(Succeed())
		Expect(deploy.Generation).To(Equal(f.appGeneration),
			"Deployment %s spec must not be changed; the missing ConfigMap must be created instead", appName)
	})
})
