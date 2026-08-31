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

package webhook

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = hubv1alpha1.AddToScheme(s)
	return s
}

func newFakeClient(objs ...client.Object) client.Reader {
	return fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).Build()
}

func hubConfig(mode hubv1alpha1.ClusterRegistryMode) *hubv1alpha1.HubConfig {
	return &hubv1alpha1.HubConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       hubv1alpha1.HubConfigSpec{ClusterRegistryMode: mode},
	}
}

func spokeWithSecret() *hubv1alpha1.SpokeCluster {
	return &hubv1alpha1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke-1"},
		Spec: hubv1alpha1.SpokeClusterSpec{
			APIServer: "https://api.spoke-1.example.com:6443",
			CredentialSource: hubv1alpha1.CredentialSource{
				Secret: &hubv1alpha1.SecretCredentialSource{
					Name:      "spoke-1-creds",
					Namespace: "openshift-lightspeed",
				},
			},
		},
	}
}

func spokeWithMCE() *hubv1alpha1.SpokeCluster {
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

func TestSpokeClusterValidator(t *testing.T) {
	tests := []struct {
		name      string
		hubConfig *hubv1alpha1.HubConfig
		spoke     *hubv1alpha1.SpokeCluster
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "secret source matches secret mode",
			hubConfig: hubConfig(hubv1alpha1.ClusterRegistryModeSecret),
			spoke:     spokeWithSecret(),
			wantErr:   false,
		},
		{
			name:      "mce source matches mce mode",
			hubConfig: hubConfig(hubv1alpha1.ClusterRegistryModeMCE),
			spoke:     spokeWithMCE(),
			wantErr:   false,
		},
		{
			name:      "mce source rejected in secret mode",
			hubConfig: hubConfig(hubv1alpha1.ClusterRegistryModeSecret),
			spoke:     spokeWithMCE(),
			wantErr:   true,
			errMsg:    "credential source does not match",
		},
		{
			name:      "secret source rejected in mce mode",
			hubConfig: hubConfig(hubv1alpha1.ClusterRegistryModeMCE),
			spoke:     spokeWithSecret(),
			wantErr:   true,
			errMsg:    "credential source does not match",
		},
		{
			name:      "rejected when HubConfig does not exist",
			hubConfig: nil,
			spoke:     spokeWithSecret(),
			wantErr:   true,
			errMsg:    "HubConfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c client.Reader
			if tt.hubConfig != nil {
				c = newFakeClient(tt.hubConfig)
			} else {
				c = newFakeClient()
			}
			v := NewSpokeClusterValidator(c)

			_, err := v.ValidateCreate(context.Background(), tt.spoke)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSpokeClusterValidatorUpdate(t *testing.T) {
	c := newFakeClient(hubConfig(hubv1alpha1.ClusterRegistryModeSecret))
	v := NewSpokeClusterValidator(c)

	_, err := v.ValidateUpdate(context.Background(), spokeWithSecret(), spokeWithSecret())
	if err != nil {
		t.Fatalf("unexpected error on valid update: %v", err)
	}

	_, err = v.ValidateUpdate(context.Background(), spokeWithSecret(), spokeWithMCE())
	if err == nil {
		t.Fatal("expected error on invalid update, got nil")
	}
}

func TestSpokeClusterValidatorDelete(t *testing.T) {
	c := newFakeClient(hubConfig(hubv1alpha1.ClusterRegistryModeSecret))
	v := NewSpokeClusterValidator(c)

	_, err := v.ValidateDelete(context.Background(), spokeWithSecret())
	if err != nil {
		t.Fatalf("unexpected error on delete: %v", err)
	}
}
