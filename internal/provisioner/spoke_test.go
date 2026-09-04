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

func TestProvision(t *testing.T) {
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
			name:              "creates all resources",
			existingObjs:      nil,
			wantErr:           false,
			validateResources: true,
		},
		{
			name: "idempotent when all resources exist",
			existingObjs: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      ServiceAccountName,
						Namespace: ManagedNamespace,
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: ClusterRoleBindingClusterReader},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "cluster-reader",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      ServiceAccountName,
							Namespace: ManagedNamespace,
						},
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: ClusterRoleBindingMonitoringView},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "cluster-monitoring-view",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      ServiceAccountName,
							Namespace: ManagedNamespace,
						},
					},
				},
			},
			wantErr:           false,
			validateResources: true,
		},
		{
			name: "idempotent when partial resources exist",
			existingObjs: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: ClusterRoleBindingClusterReader},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "cluster-reader",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      ServiceAccountName,
							Namespace: ManagedNamespace,
						},
					},
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

			err := Provision(context.Background(), c)
			if (err != nil) != tt.wantErr {
				t.Errorf("Provision() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.validateResources {
				// Verify namespace
				ns := &corev1.Namespace{}
				if err := c.Get(context.Background(), client.ObjectKey{Name: ManagedNamespace}, ns); err != nil {
					t.Errorf("Namespace %s not found: %v", ManagedNamespace, err)
				}

				// Verify ServiceAccount
				sa := &corev1.ServiceAccount{}
				if err := c.Get(context.Background(), client.ObjectKey{
					Name:      ServiceAccountName,
					Namespace: ManagedNamespace,
				}, sa); err != nil {
					t.Errorf("ServiceAccount %s/%s not found: %v", ManagedNamespace, ServiceAccountName, err)
				}

				// Verify ClusterRoleBinding cluster-reader
				crbReader := &rbacv1.ClusterRoleBinding{}
				if err := c.Get(context.Background(), client.ObjectKey{
					Name: ClusterRoleBindingClusterReader,
				}, crbReader); err != nil {
					t.Errorf("ClusterRoleBinding %s not found: %v", ClusterRoleBindingClusterReader, err)
				}

				// Verify ClusterRoleBinding cluster-monitoring-view
				crbMonitoring := &rbacv1.ClusterRoleBinding{}
				if err := c.Get(context.Background(), client.ObjectKey{
					Name: ClusterRoleBindingMonitoringView,
				}, crbMonitoring); err != nil {
					t.Errorf("ClusterRoleBinding %s not found: %v", ClusterRoleBindingMonitoringView, err)
				}
			}
		})
	}
}

func TestDeprovision(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	tests := []struct {
		name            string
		existingObjs    []client.Object
		validateDeleted bool
	}{
		{
			name: "deletes all resources",
			existingObjs: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      ServiceAccountName,
						Namespace: ManagedNamespace,
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: ClusterRoleBindingClusterReader},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "cluster-reader",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      ServiceAccountName,
							Namespace: ManagedNamespace,
						},
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: ClusterRoleBindingMonitoringView},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "cluster-monitoring-view",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      ServiceAccountName,
							Namespace: ManagedNamespace,
						},
					},
				},
			},
			validateDeleted: true,
		},
		{
			name:            "best-effort when no resources exist",
			existingObjs:    nil,
			validateDeleted: false,
		},
		{
			name: "best-effort when only some resources exist",
			existingObjs: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: ManagedNamespace},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: ClusterRoleBindingMonitoringView},
					RoleRef: rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "cluster-monitoring-view",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      ServiceAccountName,
							Namespace: ManagedNamespace,
						},
					},
				},
			},
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
			Deprovision(context.Background(), c, log)

			if tt.validateDeleted {
				// Verify namespace is deleted
				ns := &corev1.Namespace{}
				err := c.Get(context.Background(), client.ObjectKey{Name: ManagedNamespace}, ns)
				if err == nil {
					t.Errorf("Namespace %s still exists after Deprovision", ManagedNamespace)
				}

				// Verify ServiceAccount is deleted
				sa := &corev1.ServiceAccount{}
				err = c.Get(context.Background(), client.ObjectKey{
					Name:      ServiceAccountName,
					Namespace: ManagedNamespace,
				}, sa)
				if err == nil {
					t.Errorf("ServiceAccount %s/%s still exists after Deprovision", ManagedNamespace, ServiceAccountName)
				}

				// Verify ClusterRoleBinding cluster-reader is deleted
				crbReader := &rbacv1.ClusterRoleBinding{}
				err = c.Get(context.Background(), client.ObjectKey{
					Name: ClusterRoleBindingClusterReader,
				}, crbReader)
				if err == nil {
					t.Errorf("ClusterRoleBinding %s still exists after Deprovision", ClusterRoleBindingClusterReader)
				}

				// Verify ClusterRoleBinding cluster-monitoring-view is deleted
				crbMonitoring := &rbacv1.ClusterRoleBinding{}
				err = c.Get(context.Background(), client.ObjectKey{
					Name: ClusterRoleBindingMonitoringView,
				}, crbMonitoring)
				if err == nil {
					t.Errorf("ClusterRoleBinding %s still exists after Deprovision", ClusterRoleBindingMonitoringView)
				}
			}
		})
	}
}
