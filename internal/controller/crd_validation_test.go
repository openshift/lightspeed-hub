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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

var _ = Describe("HubConfig CEL Validation", func() {
	AfterEach(func() {
		hc := &hubv1alpha1.HubConfig{}
		_ = k8sClient.DeleteAllOf(ctx, hc)
	})

	It("should accept HubConfig named 'cluster' with mode 'secret'", func() {
		hc := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec:       hubv1alpha1.HubConfigSpec{ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeSecret},
		}
		Expect(k8sClient.Create(ctx, hc)).To(Succeed())
	})

	It("should accept HubConfig named 'cluster' with mode 'mce'", func() {
		hc := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: hubv1alpha1.HubConfigSpec{
				ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeMCE,
				MCE: &hubv1alpha1.MCEConfig{
					Selector: &hubv1alpha1.MCESelector{
						MatchLabels: map[string]string{"lightspeed-enabled": "true"},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, hc)).To(Succeed())
	})

	It("should reject HubConfig with name other than 'cluster'", func() {
		hc := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "not-cluster"},
			Spec:       hubv1alpha1.HubConfigSpec{ClusterRegistryMode: hubv1alpha1.ClusterRegistryModeSecret},
		}
		err := k8sClient.Create(ctx, hc)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("HubConfig name must be 'cluster'"))
	})

	It("should reject HubConfig with invalid clusterRegistryMode", func() {
		hc := &hubv1alpha1.HubConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec:       hubv1alpha1.HubConfigSpec{ClusterRegistryMode: "invalid"},
		}
		err := k8sClient.Create(ctx, hc)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("SpokeCluster CEL Validation", func() {
	AfterEach(func() {
		sc := &hubv1alpha1.SpokeCluster{}
		_ = k8sClient.DeleteAllOf(ctx, sc)
	})

	It("should accept SpokeCluster with secret credential source", func() {
		sc := &hubv1alpha1.SpokeCluster{
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
		Expect(k8sClient.Create(ctx, sc)).To(Succeed())
	})

	It("should accept SpokeCluster with mce credential source", func() {
		sc := &hubv1alpha1.SpokeCluster{
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
		Expect(k8sClient.Create(ctx, sc)).To(Succeed())
	})

	It("should reject SpokeCluster with both secret and mce", func() {
		sc := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke-3"},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: "https://api.spoke-3.example.com:6443",
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name:      "spoke-3-creds",
						Namespace: "openshift-lightspeed",
					},
					MCE: &hubv1alpha1.MCECredentialSource{
						ManagedClusterName: "spoke-3",
					},
				},
			},
		}
		err := k8sClient.Create(ctx, sc)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of secret or mce must be specified"))
	})

	It("should reject SpokeCluster with neither secret nor mce", func() {
		sc := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke-4"},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer:        "https://api.spoke-4.example.com:6443",
				CredentialSource: hubv1alpha1.CredentialSource{},
			},
		}
		err := k8sClient.Create(ctx, sc)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of secret or mce must be specified"))
	})

	It("should reject SpokeCluster with empty apiServer", func() {
		sc := &hubv1alpha1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke-5"},
			Spec: hubv1alpha1.SpokeClusterSpec{
				APIServer: "",
				CredentialSource: hubv1alpha1.CredentialSource{
					Secret: &hubv1alpha1.SecretCredentialSource{
						Name:      "spoke-5-creds",
						Namespace: "openshift-lightspeed",
					},
				},
			},
		}
		err := k8sClient.Create(ctx, sc)
		Expect(err).To(HaveOccurred())
	})
})
