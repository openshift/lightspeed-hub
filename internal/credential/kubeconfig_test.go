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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

func TestStandingKubeconfigName(t *testing.T) {
	tests := []struct {
		name      string
		spokeName string
		want      string
	}{
		{
			name:      "simple name",
			spokeName: "spoke-1",
			want:      "spoke-kubeconfig-spoke-1",
		},
		{
			name:      "name with dashes",
			spokeName: "my-spoke-cluster",
			want:      "spoke-kubeconfig-my-spoke-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StandingKubeconfigName(tt.spokeName)
			if got != tt.want {
				t.Errorf("StandingKubeconfigName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func spokeCluster() *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "spoke-1",
			UID:  types.UID("test-uid-12345"),
		},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.spoke-1.example.com:6443",
		},
	}
}

func TestBuildStandingKubeconfig_TokenAuth(t *testing.T) {
	cfg := &rest.Config{
		Host:        "https://127.0.0.1:6443",
		BearerToken: "test-token-12345",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("test-ca-data"),
		},
	}

	sc := spokeCluster()
	operatorNS := "openshift-lightspeed"

	secret, err := BuildStandingKubeconfig(cfg, sc, operatorNS)
	if err != nil {
		t.Fatalf("BuildStandingKubeconfig() error = %v", err)
	}

	// Verify secret metadata
	expectedName := "spoke-kubeconfig-spoke-1"
	if secret.Name != expectedName {
		t.Errorf("secret.Name = %q, want %q", secret.Name, expectedName)
	}
	if secret.Namespace != operatorNS {
		t.Errorf("secret.Namespace = %q, want %q", secret.Namespace, operatorNS)
	}

	// Verify owner reference
	if len(secret.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(secret.OwnerReferences))
	}
	ownerRef := secret.OwnerReferences[0]
	if ownerRef.APIVersion != hubv1alpha1.GroupVersion.String() {
		t.Errorf("owner APIVersion = %q, want %q", ownerRef.APIVersion, hubv1alpha1.GroupVersion.String())
	}
	if ownerRef.Kind != "SpokeCluster" {
		t.Errorf("owner Kind = %q, want %q", ownerRef.Kind, "SpokeCluster")
	}
	if ownerRef.Name != sc.Name {
		t.Errorf("owner Name = %q, want %q", ownerRef.Name, sc.Name)
	}
	if ownerRef.UID != sc.UID {
		t.Errorf("owner UID = %q, want %q", ownerRef.UID, sc.UID)
	}

	// Verify kubeconfig data exists
	kubeconfigBytes, ok := secret.Data[KubeconfigKey]
	if !ok {
		t.Fatalf("secret.Data missing %q key", KubeconfigKey)
	}

	// Round-trip: verify the kubeconfig is parseable and contains expected values
	roundTripCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		t.Fatalf("failed to parse generated kubeconfig: %v", err)
	}

	// Verify the standing kubeconfig uses the spoke's APIServer, not the rest.Config's Host
	if roundTripCfg.Host != sc.Spec.APIServer {
		t.Errorf("parsed config Host = %q, want %q", roundTripCfg.Host, sc.Spec.APIServer)
	}
	if roundTripCfg.BearerToken != cfg.BearerToken {
		t.Errorf("parsed config BearerToken = %q, want %q", roundTripCfg.BearerToken, cfg.BearerToken)
	}
	if string(roundTripCfg.CAData) != string(cfg.CAData) {
		t.Errorf("parsed config CAData mismatch")
	}
}

func TestBuildStandingKubeconfig_ClientCertAuth(t *testing.T) {
	cfg := &rest.Config{
		Host: "https://127.0.0.1:6443",
		TLSClientConfig: rest.TLSClientConfig{
			CAData:   []byte("test-ca-data"),
			CertData: []byte("test-cert-data"),
			KeyData:  []byte("test-key-data"),
		},
	}

	sc := spokeCluster()
	operatorNS := "openshift-lightspeed"

	secret, err := BuildStandingKubeconfig(cfg, sc, operatorNS)
	if err != nil {
		t.Fatalf("BuildStandingKubeconfig() error = %v", err)
	}

	// Verify kubeconfig data exists
	kubeconfigBytes, ok := secret.Data[KubeconfigKey]
	if !ok {
		t.Fatalf("secret.Data missing %q key", KubeconfigKey)
	}

	// Round-trip: verify the kubeconfig is parseable and contains expected values
	roundTripCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		t.Fatalf("failed to parse generated kubeconfig: %v", err)
	}

	// Verify cert auth fields
	if roundTripCfg.BearerToken != "" {
		t.Errorf("expected empty BearerToken for cert auth, got %q", roundTripCfg.BearerToken)
	}
	if string(roundTripCfg.CertData) != string(cfg.CertData) {
		t.Errorf("parsed config CertData mismatch")
	}
	if string(roundTripCfg.KeyData) != string(cfg.KeyData) {
		t.Errorf("parsed config KeyData mismatch")
	}
}

func TestBuildStandingKubeconfig_FallbackToHost(t *testing.T) {
	// When SpokeCluster.Spec.APIServer is empty, the standing kubeconfig should fall back to cfg.Host
	cfg := &rest.Config{
		Host:        "https://127.0.0.1:6443",
		BearerToken: "test-token",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("test-ca-data"),
		},
	}

	sc := spokeCluster()
	sc.Spec.APIServer = "" // Empty APIServer to test fallback
	operatorNS := "openshift-lightspeed"

	secret, err := BuildStandingKubeconfig(cfg, sc, operatorNS)
	if err != nil {
		t.Fatalf("BuildStandingKubeconfig() error = %v", err)
	}

	// Round-trip to verify
	kubeconfigBytes := secret.Data[KubeconfigKey]
	roundTripCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		t.Fatalf("failed to parse generated kubeconfig: %v", err)
	}

	// Should fall back to cfg.Host
	if roundTripCfg.Host != cfg.Host {
		t.Errorf("parsed config Host = %q, want %q (fallback to cfg.Host)", roundTripCfg.Host, cfg.Host)
	}
}
