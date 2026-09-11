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

package credential

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = hubv1alpha1.AddToScheme(scheme)
	return scheme
}

func TestAdapterCredentialName(t *testing.T) {
	got := AdapterCredentialName("prod-rosa-east")
	want := "spoke-alert-credential-prod-rosa-east"
	if got != want {
		t.Errorf("AdapterCredentialName() = %q, want %q", got, want)
	}
}

func TestBuildAdapterCredentialSecret(t *testing.T) {
	sc := &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prod-rosa-east",
			UID:  types.UID("test-uid"),
		},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.prod-rosa-east.example.com:6443",
		},
	}

	secret, err := BuildAdapterCredentialSecret(
		sc,
		"https://alertmanager-main-openshift-monitoring.apps.prod-rosa-east.example.com",
		"fake-sa-token",
		"fake-ca-bundle",
		"openshift-lightspeed",
		testScheme(),
	)
	if err != nil {
		t.Fatalf("BuildAdapterCredentialSecret() error = %v", err)
	}

	if secret.Name != "spoke-alert-credential-prod-rosa-east" {
		t.Errorf("Secret.Name = %q, want %q", secret.Name, "spoke-alert-credential-prod-rosa-east")
	}
	if secret.Namespace != "openshift-lightspeed" {
		t.Errorf("Secret.Namespace = %q, want %q", secret.Namespace, "openshift-lightspeed")
	}

	if got := string(secret.Data[AlertmanagerURLKey]); got != "https://alertmanager-main-openshift-monitoring.apps.prod-rosa-east.example.com" {
		t.Errorf("Secret.Data[%s] = %q, want alertmanager URL", AlertmanagerURLKey, got)
	}
	if got := string(secret.Data[TokenKey]); got != "fake-sa-token" {
		t.Errorf("Secret.Data[%s] = %q, want %q", TokenKey, got, "fake-sa-token")
	}
	if got := string(secret.Data[CABundleKey]); got != "fake-ca-bundle" {
		t.Errorf("Secret.Data[%s] = %q, want %q", CABundleKey, got, "fake-ca-bundle")
	}

	if len(secret.OwnerReferences) != 1 {
		t.Fatalf("Secret.OwnerReferences len = %d, want 1", len(secret.OwnerReferences))
	}
	if secret.OwnerReferences[0].Name != "prod-rosa-east" {
		t.Errorf("OwnerReference.Name = %q, want %q", secret.OwnerReferences[0].Name, "prod-rosa-east")
	}
}

func TestBuildAdapterCredentialSecretEmptyCABundle(t *testing.T) {
	sc := &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-spoke",
			UID:  types.UID("test-uid"),
		},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.test.example.com:6443",
		},
	}

	secret, err := BuildAdapterCredentialSecret(sc, "https://am.test.example.com", "token", "", "ns", testScheme())
	if err != nil {
		t.Fatalf("BuildAdapterCredentialSecret() error = %v", err)
	}

	if _, ok := secret.Data[CABundleKey]; ok {
		t.Errorf("Secret.Data should not contain %s when CA bundle is empty", CABundleKey)
	}
}
