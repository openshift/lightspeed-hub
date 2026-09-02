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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SpokeClusterSpec struct {
	// +kubebuilder:validation:MinLength=1
	APIServer string `json:"apiServer"`

	CredentialSource CredentialSource `json:"credentialSource"`
}

// +kubebuilder:validation:XValidation:rule="has(self.secret) != has(self.mce)",message="exactly one of secret or mce must be specified"
type CredentialSource struct {
	// +optional
	Secret *SecretCredentialSource `json:"secret,omitempty"`

	// +optional
	MCE *MCECredentialSource `json:"mce,omitempty"`
}

type SecretCredentialSource struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
}

type MCECredentialSource struct {
	// +kubebuilder:validation:MinLength=1
	ManagedClusterName string `json:"managedClusterName"`
}

type SpokeClusterStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type SpokeCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpokeClusterSpec   `json:"spec,omitempty"`
	Status SpokeClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SpokeClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpokeCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SpokeCluster{}, &SpokeClusterList{})
}
