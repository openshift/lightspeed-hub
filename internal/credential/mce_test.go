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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const testOperatorNS = "openshift-lightspeed"

func mceScheme() *runtime.Scheme {
	s := newScheme()
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "multicluster.openshift.io", Version: "v1", Kind: "MultiClusterEngineList"},
		&unstructured.UnstructuredList{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "multicluster.openshift.io", Version: "v1", Kind: "MultiClusterEngine"},
		&unstructured.Unstructured{},
	)
	return s
}

func newMCEFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(mceScheme()).WithObjects(objs...).Build()
}

func spokeWithMCE(name, managedCluster string) *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api." + managedCluster + ".example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{
				MCE: &hubv1alpha1.MCECredentialSource{
					ManagedClusterName: managedCluster,
				},
			},
		},
	}
}

func testMCEInstance(targetNamespace string) *unstructured.Unstructured {
	mce := &unstructured.Unstructured{}
	mce.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "multicluster.openshift.io",
		Version: "v1",
		Kind:    "MultiClusterEngine",
	})
	mce.SetName("engine")
	if targetNamespace != "" {
		_ = unstructured.SetNestedField(mce.Object, targetNamespace, "spec", "targetNamespace")
	}
	return mce
}

func testProxyService(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mceProxyServiceName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       mceProxyPortName,
					Port:       9092,
					TargetPort: intstr.FromInt32(9092),
				},
			},
		},
	}
}

func testProxyCAConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProxyCAConfigMapName,
			Namespace: testOperatorNS,
		},
		Data: map[string]string{
			proxyCAKey: "-----BEGIN CERTIFICATE-----\nfake-service-ca\n-----END CERTIFICATE-----\n",
		},
	}
}

// testMSASecret creates a ManagedServiceAccount token Secret in the spoke's
// hub namespace, matching the structure MCE produces.
func testMSASecret(managedClusterName, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MSAName,
			Namespace: managedClusterName,
		},
		Data: map[string][]byte{
			msaTokenKey: []byte(token),
			"ca.crt":    []byte("spoke-ca-data"),
		},
	}
}

func TestMCECredentialSource_GetRESTConfig(t *testing.T) {
	tests := []struct {
		name       string
		spoke      *hubv1alpha1.SpokeCluster
		objects    []client.Object
		wantErr    bool
		errMsg     string
		wantHost   string
		wantToken  string
		wantCAData string
	}{
		{
			name:  "valid MCE config returns proxy endpoint",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testMCEInstance("multicluster-engine"),
				testProxyService("multicluster-engine"),
				testProxyCAConfigMap(),
				testMSASecret("spoke-1", "msa-spoke-token-123"),
			},
			wantHost:   "https://cluster-proxy-addon-user.multicluster-engine.svc:9092/spoke-1",
			wantToken:  "msa-spoke-token-123",
			wantCAData: "-----BEGIN CERTIFICATE-----\nfake-service-ca\n-----END CERTIFICATE-----\n",
		},
		{
			name:  "MCE with default namespace when targetNamespace omitted",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testMCEInstance(""), // no targetNamespace
				testProxyService(mceDefaultNamespace),
				testProxyCAConfigMap(),
				testMSASecret("spoke-1", "msa-default-ns-token"),
			},
			wantHost:  "https://cluster-proxy-addon-user.multicluster-engine.svc:9092/spoke-1",
			wantToken: "msa-default-ns-token",
		},
		{
			name:    "no MCE credential source on spoke",
			spoke:   spokeWithSecret("admin", "default"),
			wantErr: true,
			errMsg:  "has no MCE credential source",
		},
		{
			name:  "MSA secret not found",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testMCEInstance("multicluster-engine"),
				testProxyService("multicluster-engine"),
				testProxyCAConfigMap(),
				// no MSA Secret
			},
			wantErr: true,
			errMsg:  "reading MSA token",
		},
		{
			name:  "no MCE CR found",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testProxyService("multicluster-engine"),
				testProxyCAConfigMap(),
				testMSASecret("spoke-1", "token"),
			},
			wantErr: true,
			errMsg:  "no MultiClusterEngine CR found",
		},
		{
			name:  "proxy service not found",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testMCEInstance("multicluster-engine"),
				testProxyCAConfigMap(),
				testMSASecret("spoke-1", "token"),
			},
			wantErr: true,
			errMsg:  "getting proxy service",
		},
		{
			name:  "proxy CA configmap not found",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testMCEInstance("multicluster-engine"),
				testProxyService("multicluster-engine"),
				testMSASecret("spoke-1", "token"),
			},
			wantErr: true,
			errMsg:  "reading proxy CA",
		},
		{
			name:  "proxy service missing named port",
			spoke: spokeWithMCE("spoke-1", "spoke-1"),
			objects: []client.Object{
				testMCEInstance("multicluster-engine"),
				testProxyCAConfigMap(),
				testMSASecret("spoke-1", "token"),
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      mceProxyServiceName,
						Namespace: "multicluster-engine",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{Name: "other-port", Port: 443},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "no port named",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newMCEFakeClient(tt.objects...)
			source := NewMCECredentialSource(c, testOperatorNS)

			cfg, err := source.GetRESTConfig(context.Background(), tt.spoke)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", cfg.Host, tt.wantHost)
			}
			if cfg.BearerToken != tt.wantToken {
				t.Errorf("BearerToken = %q, want %q", cfg.BearerToken, tt.wantToken)
			}
			if tt.wantCAData != "" && string(cfg.CAData) != tt.wantCAData {
				t.Errorf("CAData = %q, want %q", string(cfg.CAData), tt.wantCAData)
			}
		})
	}
}

func TestMCEStandingKubeconfigUsesProxyEndpoint(t *testing.T) {
	c := newMCEFakeClient(
		testMCEInstance("multicluster-engine"),
		testProxyService("multicluster-engine"),
		testProxyCAConfigMap(),
		testMSASecret("spoke-1", "msa-spoke-token"),
	)
	source := NewMCECredentialSource(c, testOperatorNS)

	sc := spokeWithMCE("spoke-1", "spoke-1")
	cfg, err := source.GetRESTConfig(context.Background(), sc)
	if err != nil {
		t.Fatalf("GetRESTConfig: %v", err)
	}

	// Build the standing kubeconfig with empty serverOverride (MCE mode)
	secret, err := BuildStandingKubeconfig(cfg, sc, "", testOperatorNS, kubeconfigTestScheme())
	if err != nil {
		t.Fatalf("BuildStandingKubeconfig: %v", err)
	}

	kubeconfigBytes := secret.Data[KubeconfigKey]
	if kubeconfigBytes == nil {
		t.Fatal("standing kubeconfig Secret missing kubeconfig data")
	}

	// The serialized kubeconfig should contain the proxy endpoint
	kcStr := string(kubeconfigBytes)
	if !strings.Contains(kcStr, "cluster-proxy-addon-user.multicluster-engine.svc:9092/spoke-1") {
		t.Errorf("standing kubeconfig should contain proxy endpoint, got:\n%s", kcStr)
	}
	if strings.Contains(kcStr, "api.spoke-1.example.com") {
		t.Errorf("standing kubeconfig should NOT contain spoke's real API server URL, got:\n%s", kcStr)
	}
}

func TestReadMSAToken(t *testing.T) {
	t.Run("reads token from MSA Secret", func(t *testing.T) {
		c := newMCEFakeClient(testMSASecret("spoke-1", "msa-token-value"))
		source := NewMCECredentialSource(c, testOperatorNS)
		token, err := source.readMSAToken(context.Background(), "spoke-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "msa-token-value" {
			t.Errorf("token = %q, want %q", token, "msa-token-value")
		}
	})

	t.Run("missing MSA Secret returns error", func(t *testing.T) {
		c := newMCEFakeClient() // no Secret
		source := NewMCECredentialSource(c, testOperatorNS)
		_, err := source.readMSAToken(context.Background(), "spoke-1")
		if err == nil {
			t.Fatal("expected error for missing MSA Secret")
		}
		if !strings.Contains(err.Error(), "getting MSA secret") {
			t.Errorf("error = %q, want to contain %q", err.Error(), "getting MSA secret")
		}
	})

	t.Run("empty token is rejected", func(t *testing.T) {
		c := newMCEFakeClient(testMSASecret("spoke-1", ""))
		source := NewMCECredentialSource(c, testOperatorNS)
		_, err := source.readMSAToken(context.Background(), "spoke-1")
		if err == nil {
			t.Fatal("expected error for empty token")
		}
		if !strings.Contains(err.Error(), "missing or empty") {
			t.Errorf("error = %q, want to contain %q", err.Error(), "missing or empty")
		}
	})
}

func TestFindNamedPort(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "test-ns"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80},
				{Name: "user-port", Port: 9092},
				{Name: "metrics", Port: 8080},
			},
		},
	}

	t.Run("finds existing port", func(t *testing.T) {
		port, err := findNamedPort(svc, "user-port")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 9092 {
			t.Errorf("port = %d, want 9092", port)
		}
	})

	t.Run("returns error for missing port", func(t *testing.T) {
		_, err := findNamedPort(svc, "nonexistent")
		if err == nil {
			t.Fatal("expected error for missing port")
		}
		if !strings.Contains(err.Error(), "no port named") {
			t.Errorf("error = %q, want to contain %q", err.Error(), "no port named")
		}
	})

	t.Run("returns error for empty ports", func(t *testing.T) {
		emptySvc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "ns"},
		}
		_, err := findNamedPort(emptySvc, "any")
		if err == nil {
			t.Fatal("expected error for empty ports")
		}
	})
}

func TestReadProxyCA(t *testing.T) {
	t.Run("empty CA bundle is rejected", func(t *testing.T) {
		c := newMCEFakeClient(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ProxyCAConfigMapName,
				Namespace: testOperatorNS,
			},
			Data: map[string]string{
				proxyCAKey: "   ",
			},
		})
		source := NewMCECredentialSource(c, testOperatorNS)
		_, err := source.readProxyCA(context.Background())
		if err == nil {
			t.Fatal("expected error for whitespace-only CA")
		}
		if !strings.Contains(err.Error(), "missing or empty") {
			t.Errorf("error = %q, want to contain %q", err.Error(), "missing or empty")
		}
	})

	t.Run("missing CA key is rejected", func(t *testing.T) {
		c := newMCEFakeClient(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ProxyCAConfigMapName,
				Namespace: testOperatorNS,
			},
			Data: map[string]string{
				"wrong-key": "some-data",
			},
		})
		source := NewMCECredentialSource(c, testOperatorNS)
		_, err := source.readProxyCA(context.Background())
		if err == nil {
			t.Fatal("expected error for missing CA key")
		}
	})

	t.Run("valid CA is returned", func(t *testing.T) {
		wantCA := "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"
		c := newMCEFakeClient(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ProxyCAConfigMapName,
				Namespace: testOperatorNS,
			},
			Data: map[string]string{
				proxyCAKey: wantCA,
			},
		})
		source := NewMCECredentialSource(c, testOperatorNS)
		got, err := source.readProxyCA(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != wantCA {
			t.Errorf("CA = %q, want %q", string(got), wantCA)
		}
	})
}

func TestDiscoverProxyEndpoint(t *testing.T) {
	t.Run("multiple MCE instances rejected", func(t *testing.T) {
		mce1 := testMCEInstance("ns-1")
		mce1.SetName("engine-1")
		mce2 := testMCEInstance("ns-2")
		mce2.SetName("engine-2")
		c := newMCEFakeClient(mce1, mce2)
		source := NewMCECredentialSource(c, testOperatorNS)
		_, err := source.discoverProxyEndpoint(context.Background())
		if err == nil {
			t.Fatal("expected error for multiple MCE instances")
		}
		if !strings.Contains(err.Error(), "multiple MultiClusterEngine") {
			t.Errorf("error = %q, want to contain %q", err.Error(), "multiple MultiClusterEngine")
		}
	})

	t.Run("custom targetNamespace is used", func(t *testing.T) {
		customNS := "my-mce-namespace"
		c := newMCEFakeClient(
			testMCEInstance(customNS),
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mceProxyServiceName,
					Namespace: customNS,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: mceProxyPortName, Port: 9092},
					},
				},
			},
		)
		source := NewMCECredentialSource(c, testOperatorNS)
		endpoint, err := source.discoverProxyEndpoint(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "https://cluster-proxy-addon-user." + customNS + ".svc:9092"
		if endpoint != want {
			t.Errorf("endpoint = %q, want %q", endpoint, want)
		}
	})
}

func TestDispatchingCredentialSource(t *testing.T) {
	c := newFakeClient(
		adminSecret("admin-kc", "default", map[string][]byte{
			"kubeconfig": validKubeconfigYAML(),
		}),
	)
	secretSource := NewSecretCredentialSource(c)
	mceSource := NewMCECredentialSource(c, testOperatorNS)
	dispatch := NewDispatchingCredentialSource(secretSource, mceSource)

	secretSpoke := spokeWithSecret("admin-kc", "default")
	cfg, err := dispatch.GetRESTConfig(context.Background(), secretSpoke)
	if err != nil {
		t.Fatalf("secret dispatch: %v", err)
	}
	if cfg.Host != "https://127.0.0.1:6443" {
		t.Errorf("expected host from secret kubeconfig, got %s", cfg.Host)
	}

	// MCE spoke without proper setup should fail (dispatches to MCE source)
	mceSpoke := spokeWithMCE("spoke-1", "spoke-1")
	_, err = dispatch.GetRESTConfig(context.Background(), mceSpoke)
	if err == nil {
		t.Fatal("expected MCE dispatch to fail without MCE setup")
	}

	// No credential source should fail
	emptySpoke := &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "empty"},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer:        "https://api.example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{},
		},
	}
	_, err = dispatch.GetRESTConfig(context.Background(), emptySpoke)
	if err == nil {
		t.Fatal("expected error for empty credential source")
	}
	if !strings.Contains(err.Error(), "no recognized credential source") {
		t.Errorf("expected 'no recognized credential source' error, got: %v", err)
	}
}
