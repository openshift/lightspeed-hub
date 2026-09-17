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

package controller_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/controller"
)

func hubTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = hubv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	return scheme
}

var _ = Describe("HubConfigReconciler", func() {
	const (
		testNamespace = "openshift-lightspeed"
		testImage     = "quay.io/test/adapter:v1"
	)

	It("should create adapter stack when HubConfig exists", func() {
		hc := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec:       hubv1alpha1.HubConfigSpec{ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeSecret},
		}
		fc := fake.NewClientBuilder().
			WithScheme(hubTestScheme()).
			WithObjects(hc).
			Build()

		reconciler := controller.NewHubConfigReconciler(fc, testNamespace, testImage)
		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		var dep appsv1.Deployment
		err = fc.Get(context.Background(), types.NamespacedName{
			Name: "lightspeed-hub-alerts-adapter", Namespace: testNamespace,
		}, &dep)
		Expect(err).NotTo(HaveOccurred())
		Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImage))
	})

	It("should delete adapter stack when HubConfig is deleted", func() {
		now := metav1.Now()
		hc := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "cluster",
				DeletionTimestamp: &now,
				Finalizers:        []string{"hub.openshift.io/adapter-cleanup"},
			},
			Spec: hubv1alpha1.HubConfigSpec{ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeSecret},
		}
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "lightspeed-hub-alerts-adapter", Namespace: testNamespace,
			},
		}
		fc := fake.NewClientBuilder().
			WithScheme(hubTestScheme()).
			WithObjects(hc, dep).
			Build()

		reconciler := controller.NewHubConfigReconciler(fc, testNamespace, testImage)
		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())

		var deletedDep appsv1.Deployment
		err = fc.Get(context.Background(), types.NamespacedName{
			Name: "lightspeed-hub-alerts-adapter", Namespace: testNamespace,
		}, &deletedDep)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("should be no-op when HubConfig is not found", func() {
		fc := fake.NewClientBuilder().WithScheme(hubTestScheme()).Build()
		reconciler := controller.NewHubConfigReconciler(fc, testNamespace, testImage)
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "cluster"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
	})
})
