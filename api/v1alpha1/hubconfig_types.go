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

type ClusterRegistryMode string

const (
	ClusterRegistryModeSecret ClusterRegistryMode = "secret"
	ClusterRegistryModeMCE    ClusterRegistryMode = "mce"
)

type HubConfigSpec struct {
	// +kubebuilder:validation:Enum=secret;mce
	ClusterRegistryMode ClusterRegistryMode `json:"clusterRegistryMode"`

	// +optional
	MCE *MCEConfig `json:"mce,omitempty"`
}

type MCEConfig struct {
	// +optional
	Selector *MCESelector `json:"selector,omitempty"`
}

type MCESelector struct {
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

type HubConfigStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="HubConfig name must be 'cluster'"
type HubConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HubConfigSpec   `json:"spec,omitempty"`
	Status HubConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type HubConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HubConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HubConfig{}, &HubConfigList{})
}
