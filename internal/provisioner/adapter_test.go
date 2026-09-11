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

package provisioner

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProvisionAdapter(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	tests := []struct {
		name              string
		existingObjs      []client.Object
		wantErr           bool
		validateResources bool
	}{
		{
			name: "creates all adapter resources",
			existingObjs: []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace}},
			},
			wantErr:           false,
			validateResources: true,
		},
		{
			name: "idempotent when all adapter resources exist",
			existingObjs: []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace}},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AlertAdapterServiceAccount,
						Namespace: ManagedNamespace,
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AlertAdapterRoleBinding,
						Namespace: MonitoringNamespace,
					},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "Role",
						Name:     MonitoringAlertmanagerViewRole,
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      AlertAdapterServiceAccount,
							Namespace: ManagedNamespace,
						},
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AlertAdapterTokenSecret,
						Namespace: ManagedNamespace,
						Annotations: map[string]string{
							"kubernetes.io/service-account.name": AlertAdapterServiceAccount,
						},
					},
					Type: corev1.SecretTypeServiceAccountToken,
				},
			},
			wantErr:           false,
			validateResources: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjs...).
				Build()

			err := ProvisionAdapter(context.Background(), c)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProvisionAdapter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.validateResources {
				// Verify ServiceAccount
				sa := &corev1.ServiceAccount{}
				if err := c.Get(context.Background(), client.ObjectKey{
					Name:      AlertAdapterServiceAccount,
					Namespace: ManagedNamespace,
				}, sa); err != nil {
					t.Errorf("ServiceAccount %s/%s not found: %v", ManagedNamespace, AlertAdapterServiceAccount, err)
				}

				// Verify RoleBinding in openshift-monitoring
				rb := &rbacv1.RoleBinding{}
				if err := c.Get(context.Background(), client.ObjectKey{
					Name:      AlertAdapterRoleBinding,
					Namespace: MonitoringNamespace,
				}, rb); err != nil {
					t.Errorf("RoleBinding %s/%s not found: %v", MonitoringNamespace, AlertAdapterRoleBinding, err)
				} else {
					if rb.RoleRef.Name != MonitoringAlertmanagerViewRole {
						t.Errorf("RoleBinding roleRef.name = %q, want %q", rb.RoleRef.Name, MonitoringAlertmanagerViewRole)
					}
					if rb.RoleRef.Kind != "Role" {
						t.Errorf("RoleBinding roleRef.kind = %q, want %q", rb.RoleRef.Kind, "Role")
					}
					if len(rb.Subjects) != 1 || rb.Subjects[0].Name != AlertAdapterServiceAccount || rb.Subjects[0].Namespace != ManagedNamespace {
						t.Errorf("RoleBinding subjects unexpected: %+v", rb.Subjects)
					}
				}

				// Verify token Secret
				tokenSecret := &corev1.Secret{}
				if err := c.Get(context.Background(), client.ObjectKey{
					Name:      AlertAdapterTokenSecret,
					Namespace: ManagedNamespace,
				}, tokenSecret); err != nil {
					t.Errorf("Token Secret %s/%s not found: %v", ManagedNamespace, AlertAdapterTokenSecret, err)
				} else {
					if tokenSecret.Type != corev1.SecretTypeServiceAccountToken {
						t.Errorf("Token Secret type = %q, want %q", tokenSecret.Type, corev1.SecretTypeServiceAccountToken)
					}
					ann := tokenSecret.Annotations["kubernetes.io/service-account.name"]
					if ann != AlertAdapterServiceAccount {
						t.Errorf("Token Secret annotation service-account.name = %q, want %q", ann, AlertAdapterServiceAccount)
					}
				}
			}
		})
	}
}

func TestDeprovisionAdapter(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	tests := []struct {
		name            string
		existingObjs    []client.Object
		validateDeleted bool
	}{
		{
			name: "deletes all adapter resources",
			existingObjs: []client.Object{
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AlertAdapterServiceAccount,
						Namespace: ManagedNamespace,
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AlertAdapterRoleBinding,
						Namespace: MonitoringNamespace,
					},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "Role",
						Name:     MonitoringAlertmanagerViewRole,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AlertAdapterTokenSecret,
						Namespace: ManagedNamespace,
					},
					Type: corev1.SecretTypeServiceAccountToken,
				},
			},
			validateDeleted: true,
		},
		{
			name:            "best-effort when no adapter resources exist",
			existingObjs:    nil,
			validateDeleted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjs...).
				Build()

			log := logr.Discard()
			DeprovisionAdapter(context.Background(), c, log)

			if tt.validateDeleted {
				sa := &corev1.ServiceAccount{}
				err := c.Get(context.Background(), client.ObjectKey{
					Name: AlertAdapterServiceAccount, Namespace: ManagedNamespace,
				}, sa)
				if err == nil {
					t.Errorf("ServiceAccount %s/%s still exists after DeprovisionAdapter", ManagedNamespace, AlertAdapterServiceAccount)
				}

				rb := &rbacv1.RoleBinding{}
				err = c.Get(context.Background(), client.ObjectKey{
					Name: AlertAdapterRoleBinding, Namespace: MonitoringNamespace,
				}, rb)
				if err == nil {
					t.Errorf("RoleBinding %s/%s still exists after DeprovisionAdapter", MonitoringNamespace, AlertAdapterRoleBinding)
				}

				tokenSecret := &corev1.Secret{}
				err = c.Get(context.Background(), client.ObjectKey{
					Name: AlertAdapterTokenSecret, Namespace: ManagedNamespace,
				}, tokenSecret)
				if err == nil {
					t.Errorf("Token Secret %s/%s still exists after DeprovisionAdapter", ManagedNamespace, AlertAdapterTokenSecret)
				}
			}
		})
	}
}
