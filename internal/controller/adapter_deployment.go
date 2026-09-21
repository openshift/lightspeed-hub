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

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	adapterName             = "lightspeed-hub-alerts-adapter"
	adapterConfigMapName    = "hub-alerts-adapter-config"
	adapterClusterRoleName  = "lightspeed-hub-alerts-adapter-cluster"
	adapterMonitoringRBName = "lightspeed-hub-alerts-adapter-alertmanager"

	adapterRestartAnnotation = "hub.openshift.io/restart-timestamp"
)

func adapterLabels() map[string]string {
	return map[string]string{"app": adapterName}
}

func adapterServiceAccount(namespace string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterName,
			Namespace: namespace,
		},
	}
}

func adapterRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterName,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"agentic.openshift.io"},
				Resources: []string{"agenticruns"},
				Verbs:     []string{"create", "list", "get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get"},
			},
		},
	}
}

func adapterRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterName,
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     adapterName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      adapterName,
				Namespace: namespace,
			},
		},
	}
}

func adapterMonitoringRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterMonitoringRBName,
			Namespace: "openshift-monitoring",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     "monitoring-alertmanager-view",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      adapterName,
				Namespace: namespace,
			},
		},
	}
}

func adapterClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: adapterClusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"agentic.openshift.io"},
				Resources:     []string{"agenticolsconfigs"},
				ResourceNames: []string{"cluster"},
				Verbs:         []string{"get"},
			},
			{
				APIGroups: []string{"hub.openshift.io"},
				Resources: []string{"spokeclusters"},
				Verbs:     []string{"list", "watch"},
			},
		},
	}
}

func adapterClusterRoleBinding(namespace string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: adapterClusterRoleName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     adapterClusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      adapterName,
				Namespace: namespace,
			},
		},
	}
}

func adapterConfigMap(namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterConfigMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"config.yaml": `filtering:
  allowedReceivers: []
`,
		},
	}
}

func adapterDeployment(namespace, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterName,
			Namespace: namespace,
			Labels:    adapterLabels(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: adapterLabels(),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: adapterLabels(),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: adapterName,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: adapterConfigMapName,
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "adapter",
							Image: image,
							Args:  []string{"--multicluster"},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/etc/alerts-adapter",
									ReadOnly:  true,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ALERTMANAGER_URL",
									Value: "",
								},
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

type adapterResource struct {
	kind     string
	resource client.Object
	update   bool // whether to update on AlreadyExists (RBAC yes, ConfigMap no)
}

func adapterResources(namespace string) []adapterResource {
	return []adapterResource{
		{"ServiceAccount", adapterServiceAccount(namespace), false},
		{"Role", adapterRole(namespace), true},
		{"RoleBinding", adapterRoleBinding(namespace), true},
		{"RoleBinding", adapterMonitoringRoleBinding(namespace), true},
		{"ClusterRole", adapterClusterRole(), true},
		{"ClusterRoleBinding", adapterClusterRoleBinding(namespace), true},
		{"ConfigMap", adapterConfigMap(namespace), false},
	}
}

func ensureAdapterStack(ctx context.Context, c client.Client, namespace, image string) error {
	for _, ar := range adapterResources(namespace) {
		if err := c.Create(ctx, ar.resource); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating %s %s: %w", ar.kind, ar.resource.GetName(), err)
			}
			if ar.update {
				if err := ensureAdapterResource(ctx, c, ar); err != nil {
					return err
				}
			}
		}
	}

	return ensureAdapterDeployment(ctx, c, namespace, image)
}

func ensureAdapterResource(ctx context.Context, c client.Client, ar adapterResource) error {
	existing := ar.resource.DeepCopyObject().(client.Object)
	if err := c.Get(ctx, client.ObjectKeyFromObject(ar.resource), existing); err != nil {
		return fmt.Errorf("getting existing %s %s: %w", ar.kind, ar.resource.GetName(), err)
	}
	ar.resource.SetResourceVersion(existing.GetResourceVersion())
	if err := c.Update(ctx, ar.resource); err != nil {
		return fmt.Errorf("updating %s %s: %w", ar.kind, ar.resource.GetName(), err)
	}
	return nil
}

func ensureAdapterDeployment(ctx context.Context, c client.Client, namespace, image string) error {
	desired := adapterDeployment(namespace, image)

	err := c.Create(ctx, desired)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Deployment %s: %w", desired.Name, err)
	}

	var existing appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKeyFromObject(desired), &existing); err != nil {
		return fmt.Errorf("getting existing Deployment %s: %w", desired.Name, err)
	}

	if len(existing.Spec.Template.Spec.Containers) > 0 && existing.Spec.Template.Spec.Containers[0].Image != image {
		existing.Spec.Template.Spec.Containers[0].Image = image
		if err := c.Update(ctx, &existing); err != nil {
			return fmt.Errorf("updating Deployment %s image: %w", desired.Name, err)
		}
	}
	return nil
}

func teardownAdapterStack(ctx context.Context, c client.Client, namespace string) error {
	var firstErr error
	resources := []adapterResource{
		{"Deployment", adapterDeployment(namespace, ""), false},
		{"ConfigMap", adapterConfigMap(namespace), false},
		{"ClusterRoleBinding", adapterClusterRoleBinding(namespace), false},
		{"ClusterRole", adapterClusterRole(), false},
		{"RoleBinding", adapterMonitoringRoleBinding(namespace), false},
		{"RoleBinding", adapterRoleBinding(namespace), false},
		{"Role", adapterRole(namespace), false},
		{"ServiceAccount", adapterServiceAccount(namespace), false},
	}

	for _, ar := range resources {
		if err := c.Delete(ctx, ar.resource); err != nil {
			if !apierrors.IsNotFound(err) {
				logf.FromContext(ctx).Error(err, "failed to delete adapter resource", "kind", ar.kind, "name", ar.resource.GetName())
				if firstErr == nil {
					firstErr = fmt.Errorf("deleting %s %s: %w", ar.kind, ar.resource.GetName(), err)
				}
			}
		}
	}
	return firstErr
}

func restartAdapterDeployment(ctx context.Context, c client.Client, namespace string) error {
	var dep appsv1.Deployment
	key := client.ObjectKey{Name: adapterName, Namespace: namespace}
	if err := c.Get(ctx, key, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting adapter Deployment for restart: %w", err)
	}

	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations[adapterRestartAnnotation] = time.Now().UTC().Format(time.RFC3339)

	if err := c.Update(ctx, &dep); err != nil {
		return fmt.Errorf("updating adapter Deployment for restart: %w", err)
	}
	return nil
}
