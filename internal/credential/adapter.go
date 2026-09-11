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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	AdapterCredentialPrefix = "spoke-alert-credential-"

	AlertmanagerURLKey = "alertmanager-url"
	TokenKey           = "token"
	CABundleKey        = "ca-bundle"

	AdapterCredentialLabel = "hub.openshift.io/alert-credential-secret"
)

// AdapterCredentialName returns the hub-side Secret name for a spoke's adapter credentials.
func AdapterCredentialName(spokeName string) string {
	return AdapterCredentialPrefix + spokeName
}

// BuildAdapterCredentialSecret constructs a hub-side Secret containing the adapter's
// connection details for a spoke: AlertManager Route URL, SA bearer token, and
// optionally the spoke's ingress CA for TLS verification. The Secret has a controller
// owner reference to the SpokeCluster CR for automatic garbage collection.
func BuildAdapterCredentialSecret(
	sc *hubv1alpha1.SpokeCluster,
	alertmanagerURL string,
	token string,
	caBundle string,
	operatorNamespace string,
	scheme *runtime.Scheme,
) (*corev1.Secret, error) {
	data := map[string][]byte{
		AlertmanagerURLKey: []byte(alertmanagerURL),
		TokenKey:           []byte(token),
	}
	if caBundle != "" {
		data[CABundleKey] = []byte(caBundle)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AdapterCredentialName(sc.Name),
			Namespace: operatorNamespace,
		},
		Data: data,
	}

	if err := controllerutil.SetControllerReference(sc, secret, scheme); err != nil {
		return nil, fmt.Errorf("setting controller reference on adapter credential for spoke %q: %w", sc.Name, err)
	}

	return secret, nil
}
