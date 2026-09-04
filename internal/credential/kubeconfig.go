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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	StandingKubeconfigPrefix = "spoke-kubeconfig-"
	KubeconfigKey            = "kubeconfig"
)

func StandingKubeconfigName(spokeName string) string {
	return StandingKubeconfigPrefix + spokeName
}

func BuildStandingKubeconfig(cfg *rest.Config, sc *hubv1alpha1.SpokeCluster, operatorNamespace string) (*corev1.Secret, error) {
	kubeconfig := buildKubeconfigAPI(cfg, sc.Spec.APIServer)

	kubeconfigBytes, err := clientcmd.Write(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("serializing standing kubeconfig for spoke %q: %w", sc.Name, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      StandingKubeconfigName(sc.Name),
			Namespace: operatorNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: hubv1alpha1.GroupVersion.String(),
					Kind:       "SpokeCluster",
					Name:       sc.Name,
					UID:        sc.UID,
				},
			},
		},
		Data: map[string][]byte{
			KubeconfigKey: kubeconfigBytes,
		},
	}

	return secret, nil
}

func buildKubeconfigAPI(cfg *rest.Config, apiServer string) clientcmdapi.Config {
	cluster := clientcmdapi.NewCluster()
	cluster.Server = apiServer
	cluster.CertificateAuthorityData = cfg.CAData
	if cluster.Server == "" {
		cluster.Server = cfg.Host
	}

	user := clientcmdapi.NewAuthInfo()
	if cfg.BearerToken != "" {
		user.Token = cfg.BearerToken
	}
	if len(cfg.CertData) > 0 {
		user.ClientCertificateData = cfg.CertData
		user.ClientKeyData = cfg.KeyData
	}

	return clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"spoke": cluster},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"spoke-user": user},
		Contexts:       map[string]*clientcmdapi.Context{"spoke": {Cluster: "spoke", AuthInfo: "spoke-user"}},
		CurrentContext: "spoke",
	}
}
