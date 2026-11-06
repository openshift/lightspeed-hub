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
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

const (
	// MCE proxy service constants
	mceProxyServiceName = "cluster-proxy-addon-user"
	mceProxyPortName    = "user-port"

	// ProxyCAConfigMapName is the ConfigMap that receives the service-ca bundle
	// via the service.beta.openshift.io/inject-cabundle annotation. Created by
	// deployment configuration (kustomize), not by the controller.
	ProxyCAConfigMapName = "lightspeed-hub-proxy-ca"
	proxyCAKey           = "service-ca.crt"

	// MCE discovery label applied to auto-created SpokeCluster CRs
	MCEManagedByLabel = "hub.openshift.io/managed-by"
	MCEManagedByValue = "mce-auto-discovery"

	// MSAName is the ManagedServiceAccount name created per spoke.
	// MCE creates a corresponding SA on the spoke and stores its token
	// in a Secret with the same name in the spoke's hub namespace.
	MSAName     = "lightspeed-hub-agent"
	msaTokenKey = "token"

	// MCE default namespace when targetNamespace is omitted
	mceDefaultNamespace = "multicluster-engine"
)

// MCECredentialSource reads a ManagedServiceAccount token from the spoke's
// hub namespace and combines it with the MCE cluster-proxy endpoint to return
// a rest.Config for spoke access. MCE handles token lifecycle (creation,
// rotation) — this source is just a Secret reader, parallel to
// SecretCredentialSource.
//
// The MSA token is spoke-issued, so the cluster-proxy passes it through
// without impersonation. The identity on the spoke is a normal SA
// (e.g. system:serviceaccount:open-cluster-management-agent-addon:lightspeed-hub-agent),
// not an impersonated cluster:hub: identity.
type MCECredentialSource struct {
	hubClient         client.Reader
	operatorNamespace string
}

func NewMCECredentialSource(hubClient client.Reader, operatorNamespace string) *MCECredentialSource {
	return &MCECredentialSource{
		hubClient:         hubClient,
		operatorNamespace: operatorNamespace,
	}
}

func (m *MCECredentialSource) GetRESTConfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error) {
	if sc.Spec.CredentialSource.MCE == nil {
		return nil, fmt.Errorf("spoke %q has no MCE credential source", sc.Name)
	}

	managedClusterName := sc.Spec.CredentialSource.MCE.ManagedClusterName

	// Read the MSA token from the spoke's namespace on the hub.
	// MCE creates and rotates this Secret automatically.
	token, err := m.readMSAToken(ctx, managedClusterName)
	if err != nil {
		return nil, fmt.Errorf("reading MSA token for spoke %q: %w", managedClusterName, err)
	}

	// Discover the MCE proxy endpoint
	proxyHost, err := m.discoverProxyEndpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovering MCE proxy endpoint: %w", err)
	}

	// Read the proxy CA for TLS verification — the proxy's serving cert is
	// signed by the cluster service-ca, not a public CA.
	caData, err := m.readProxyCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading proxy CA: %w", err)
	}

	// The MCE cluster-proxy user service is a path-based HTTPS reverse proxy.
	// Requests target https://<endpoint>/<managed-cluster-name>/<api-path>.
	// Set Host to include the cluster name prefix so client-go appends API
	// paths after it.
	serverURL := proxyHost + "/" + managedClusterName

	return &rest.Config{
		Host:        serverURL,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: caData,
		},
	}, nil
}

// readMSAToken reads the ManagedServiceAccount token Secret from the spoke's
// namespace on the hub. The Secret is created and rotated by MCE's
// managed-serviceaccount addon.
func (m *MCECredentialSource) readMSAToken(ctx context.Context, managedClusterName string) (string, error) {
	var secret corev1.Secret
	key := client.ObjectKey{
		Name:      MSAName,
		Namespace: managedClusterName, // spoke's namespace on hub = ManagedCluster name
	}
	if err := m.hubClient.Get(ctx, key, &secret); err != nil {
		return "", fmt.Errorf("getting MSA secret %s/%s: %w", key.Namespace, key.Name, err)
	}

	token, ok := secret.Data[msaTokenKey]
	if !ok || len(token) == 0 {
		return "", fmt.Errorf("MSA secret %s/%s missing or empty %q key", key.Namespace, key.Name, msaTokenKey)
	}

	return strings.TrimSpace(string(token)), nil
}

// discoverProxyEndpoint finds the MCE MultiClusterEngine CR, reads its
// targetNamespace, and constructs the cluster-proxy user service DNS endpoint.
func (m *MCECredentialSource) discoverProxyEndpoint(ctx context.Context) (string, error) {
	// Find the MultiClusterEngine instance
	mceList := &unstructured.UnstructuredList{}
	mceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "multicluster.openshift.io",
		Version: "v1",
		Kind:    "MultiClusterEngineList",
	})
	if err := m.hubClient.List(ctx, mceList); err != nil {
		return "", fmt.Errorf("listing MultiClusterEngine CRs: %w", err)
	}

	if len(mceList.Items) == 0 {
		return "", fmt.Errorf("no MultiClusterEngine CR found")
	}
	if len(mceList.Items) > 1 {
		return "", fmt.Errorf("multiple MultiClusterEngine CRs found, expected exactly one")
	}

	mce := &mceList.Items[0]

	// Read targetNamespace, fall back to default
	targetNS, _, _ := unstructured.NestedString(mce.Object, "spec", "targetNamespace")
	if targetNS == "" {
		targetNS = mceDefaultNamespace
	}

	// Read the proxy service to get the actual port
	var svc corev1.Service
	svcKey := client.ObjectKey{Name: mceProxyServiceName, Namespace: targetNS}
	if err := m.hubClient.Get(ctx, svcKey, &svc); err != nil {
		return "", fmt.Errorf("getting proxy service %s/%s: %w", svcKey.Namespace, svcKey.Name, err)
	}

	port, err := findNamedPort(&svc, mceProxyPortName)
	if err != nil {
		return "", err
	}

	return "https://" + mceProxyServiceName + "." + targetNS + ".svc:" + strconv.Itoa(int(port)), nil
}

// readProxyCA reads the service-ca bundle from the proxy CA ConfigMap.
func (m *MCECredentialSource) readProxyCA(ctx context.Context) ([]byte, error) {
	var cm corev1.ConfigMap
	key := client.ObjectKey{
		Name:      ProxyCAConfigMapName,
		Namespace: m.operatorNamespace,
	}
	if err := m.hubClient.Get(ctx, key, &cm); err != nil {
		return nil, fmt.Errorf("getting proxy CA configmap %s/%s: %w", key.Namespace, key.Name, err)
	}

	caBundle, ok := cm.Data[proxyCAKey]
	if !ok || strings.TrimSpace(caBundle) == "" {
		return nil, fmt.Errorf("proxy CA configmap %s/%s missing or empty %q key (service-ca operator may not have injected the CA yet)",
			key.Namespace, key.Name, proxyCAKey)
	}

	return []byte(caBundle), nil
}

// findNamedPort returns the port number for the named port on a Service.
func findNamedPort(svc *corev1.Service, portName string) (int32, error) {
	for _, p := range svc.Spec.Ports {
		if p.Name == portName {
			return p.Port, nil
		}
	}
	return 0, fmt.Errorf("proxy service %s/%s has no port named %q", svc.Namespace, svc.Name, portName)
}

// DispatchingCredentialSource routes GetRESTConfig to the appropriate source
// based on the SpokeCluster's credential source type.
type DispatchingCredentialSource struct {
	secret *SecretCredentialSource
	mce    *MCECredentialSource
}

func NewDispatchingCredentialSource(secret *SecretCredentialSource, mce *MCECredentialSource) *DispatchingCredentialSource {
	return &DispatchingCredentialSource{secret: secret, mce: mce}
}

func (d *DispatchingCredentialSource) GetRESTConfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error) {
	switch {
	case sc.Spec.CredentialSource.Secret != nil:
		return d.secret.GetRESTConfig(ctx, sc)
	case sc.Spec.CredentialSource.MCE != nil:
		return d.mce.GetRESTConfig(ctx, sc)
	default:
		return nil, fmt.Errorf("spoke %q has no recognized credential source", sc.Name)
	}
}
