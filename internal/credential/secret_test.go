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
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = hubv1alpha1.AddToScheme(s)
	return s
}

func newFakeClient(objs ...client.Object) client.Reader {
	return fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).Build()
}

func validKubeconfigYAML() []byte {
	return []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJkakNDQVIyZ0F3SUJBZ0lCQURBS0JnZ3Foa2pPUFFRREFqQWpNU0V3SHdZRFZRUUREQmhyTTNNdGMyVnkKZG1WeUxXTmhRREUzTXpjek1ERXhOekF3SGhjTk1qUXdPVEEwTURJd05URXdXaGNOTXpRd09UQXlNREl3TlRFdwpXakFqTVNFd0h3WURWUVFEREJock0zTXRjMlZ5ZG1WeUxXTmhRREUzTXpjek1ERXhOekF3V1RBVEJnY3Foa2pPClBRSUJCZ2dxaGtqT1BRTUJCd05DQUFUZEJTNVl5S1E3UG1UdDB1UVNlOGJya3lQaGVLQ1IvZ0JzT3dZRVpPUmsKam5BZlBiL0xlUmxIN2pWTmpNbnFYc1E5VjJtSFlXalFIY1RJRWVUYjZFY3hvMEl3UURBT0JnTlZIUThCQWY4RQpCQU1DQXFRd0R3WURWUjBUQVFIL0JBVXdBd0VCL3pBZEJnTlZIUTRFRmdRVVpvcElhcFM4VVBxUFJYN0VNZ0dICjY0UWlrR0V3Q2dZSUtvWkl6ajBFQXdJRFJ3QXdSQUlnVHJmOE5nM0JnZ2FDTVZlMnE0aUF2cnpsMzd2Z3NqWnQKL1htdWpJK1FHQVlDSURDT0xxTHJuV3oxb0R5Y0hGeWhBQmJQVGtLaXBOT2ljdnpvVTFYczFid3AKLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo=
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user:
    token: eyJhbGciOiJSUzI1NiIsImtpZCI6IlRoaXNJc0FGYW5jeUtleUlEIn0.eyJpc3MiOiJrdWJlcm5ldGVzL3NlcnZpY2VhY2NvdW50Iiwic3ViIjoic3lzdGVtOnNlcnZpY2VhY2NvdW50Om9wZW5zaGlmdC1saWdodHNwZWVkOmRlZmF1bHQifQ.signature
`)
}

func spokeWithSecret(name, namespace string) *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke-1"},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.spoke-1.example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{
				Secret: &hubv1alpha1.SecretCredentialSource{
					Name:      name,
					Namespace: namespace,
				},
			},
		},
	}
}

func spokeWithNoSecret() *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke-2"},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.spoke-2.example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{
				MCE: &hubv1alpha1.MCECredentialSource{
					ManagedClusterName: "spoke-2",
				},
			},
		},
	}
}

func adminSecret(name, namespace string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
	}
}

func TestSecretCredentialSource_GetRESTConfig(t *testing.T) {
	tests := []struct {
		name    string
		spoke   *hubv1alpha1.SpokeCluster
		secrets []client.Object
		wantErr bool
		errMsg  string
	}{
		{
			name:  "valid kubeconfig",
			spoke: spokeWithSecret("admin-kubeconfig", "openshift-lightspeed"),
			secrets: []client.Object{
				adminSecret("admin-kubeconfig", "openshift-lightspeed", map[string][]byte{
					"kubeconfig": validKubeconfigYAML(),
				}),
			},
			wantErr: false,
		},
		{
			name:    "secret not found",
			spoke:   spokeWithSecret("missing-secret", "openshift-lightspeed"),
			secrets: []client.Object{},
			wantErr: true,
			errMsg:  "reading admin kubeconfig secret",
		},
		{
			name:  "missing kubeconfig key",
			spoke: spokeWithSecret("admin-kubeconfig", "openshift-lightspeed"),
			secrets: []client.Object{
				adminSecret("admin-kubeconfig", "openshift-lightspeed", map[string][]byte{
					"data": []byte("some other data"),
				}),
			},
			wantErr: true,
			errMsg:  "missing 'kubeconfig' key",
		},
		{
			name:    "nil secret credential source",
			spoke:   spokeWithNoSecret(),
			secrets: []client.Object{},
			wantErr: true,
			errMsg:  "has no secret credential source",
		},
		{
			name:  "invalid kubeconfig data",
			spoke: spokeWithSecret("admin-kubeconfig", "openshift-lightspeed"),
			secrets: []client.Object{
				adminSecret("admin-kubeconfig", "openshift-lightspeed", map[string][]byte{
					"kubeconfig": []byte("invalid yaml data"),
				}),
			},
			wantErr: true,
			errMsg:  "parsing kubeconfig from secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeClient(tt.secrets...)
			source := NewSecretCredentialSource(c)

			cfg, err := source.GetRESTConfig(context.Background(), tt.spoke)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg == nil {
					t.Fatal("expected non-nil rest.Config, got nil")
				}
				// Verify the config has expected fields from the valid kubeconfig
				if cfg.Host != "https://127.0.0.1:6443" {
					t.Errorf("expected host https://127.0.0.1:6443, got %s", cfg.Host)
				}
				if cfg.BearerToken == "" {
					t.Error("expected non-empty bearer token")
				}
			}
		})
	}
}
