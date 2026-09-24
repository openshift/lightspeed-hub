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
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const operatorNamespace = "openshift-lightspeed"

var (
	ctx = context.Background()

	hubClient    client.Client
	spoke1Client client.Client
	spoke2Client client.Client

	spoke1InternalKubeconfig []byte
	spoke2InternalKubeconfig []byte

	spoke1APIServer string
	spoke2APIServer string

	spoke1ContainerName string
	spoke2ContainerName string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multicluster E2E Suite")
}

var _ = BeforeSuite(func() {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(hubv1alpha1.AddToScheme(scheme)).To(Succeed())

	hubClient = mustBuildClient(scheme, requireEnv("MC_HUB_KUBECONFIG"))

	spokePaths := strings.Split(requireEnv("MC_SPOKE_KUBECONFIGS"), ",")
	Expect(spokePaths).To(HaveLen(2), "MC_SPOKE_KUBECONFIGS must have exactly 2 comma-separated paths")
	spoke1Client = mustBuildClient(scheme, strings.TrimSpace(spokePaths[0]))
	spoke2Client = mustBuildClient(scheme, strings.TrimSpace(spokePaths[1]))

	internalPaths := strings.Split(requireEnv("MC_SPOKE_INTERNAL_KUBECONFIGS"), ",")
	Expect(internalPaths).To(HaveLen(2), "MC_SPOKE_INTERNAL_KUBECONFIGS must have exactly 2 comma-separated paths")

	var err error
	spoke1InternalKubeconfig, err = os.ReadFile(strings.TrimSpace(internalPaths[0]))
	Expect(err).NotTo(HaveOccurred(), "reading spoke1 internal kubeconfig")
	spoke2InternalKubeconfig, err = os.ReadFile(strings.TrimSpace(internalPaths[1]))
	Expect(err).NotTo(HaveOccurred(), "reading spoke2 internal kubeconfig")

	spoke1APIServer = mustExtractServer(spoke1InternalKubeconfig)
	spoke2APIServer = mustExtractServer(spoke2InternalKubeconfig)

	// Container names are optional — only present in T1 (kind)
	if containers := os.Getenv("MC_SPOKE_CONTAINER_NAMES"); containers != "" {
		parts := strings.Split(containers, ",")
		Expect(parts).To(HaveLen(2), "MC_SPOKE_CONTAINER_NAMES must have exactly 2 entries")
		spoke1ContainerName = strings.TrimSpace(parts[0])
		spoke2ContainerName = strings.TrimSpace(parts[1])
	}
})

func requireEnv(key string) string {
	v := os.Getenv(key)
	Expect(v).NotTo(BeEmpty(), "required env var %s is not set", key)
	return v
}

func mustBuildClient(scheme *runtime.Scheme, kubeconfigPath string) client.Client {
	absPath, err := filepath.Abs(kubeconfigPath)
	Expect(err).NotTo(HaveOccurred())
	cfg, err := clientcmd.BuildConfigFromFlags("", absPath)
	Expect(err).NotTo(HaveOccurred(), "building REST config from %s", kubeconfigPath)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "creating client from %s", kubeconfigPath)
	return c
}

func mustExtractServer(kubeconfigBytes []byte) string {
	apiCfg, err := clientcmd.Load(kubeconfigBytes)
	Expect(err).NotTo(HaveOccurred(), "loading kubeconfig to extract server")
	for _, cluster := range apiCfg.Clusters {
		Expect(cluster.Server).NotTo(BeEmpty(), "kubeconfig cluster has empty server")
		return cluster.Server
	}
	Fail("no cluster found in internal kubeconfig")
	return ""
}
