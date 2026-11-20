//go:build mc_e2e || mc_product_e2e || alert_pipeline_e2e

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

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Shared by every e2e suite in this package; each suite is selected by its own build tag.

const operatorNamespace = "openshift-lightspeed"

var (
	ctx = context.Background()

	hubClient client.Client
)

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
