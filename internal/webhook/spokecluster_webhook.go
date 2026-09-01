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

package webhook

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

var (
	ErrHubConfigNotFound        = errors.New("HubConfig 'cluster' not found: create a HubConfig before registering spoke clusters")
	ErrCredentialSourceMismatch = errors.New("credential source does not match HubConfig clusterRegistryMode")
)

// +kubebuilder:webhook:path=/validate-hub-openshift-io-v1alpha1-spokecluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=hub.openshift.io,resources=spokeclusters,verbs=create;update,versions=v1alpha1,name=vspokecluster.hub.openshift.io,admissionReviewVersions=v1

type SpokeClusterValidator struct {
	client client.Reader
}

func NewSpokeClusterValidator(c client.Reader) *SpokeClusterValidator {
	return &SpokeClusterValidator{client: c}
}

func (v *SpokeClusterValidator) ValidateCreate(ctx context.Context, obj *hubv1alpha1.SpokeCluster) (admission.Warnings, error) {
	return v.validate(ctx, obj)
}

func (v *SpokeClusterValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *hubv1alpha1.SpokeCluster) (admission.Warnings, error) {
	return v.validate(ctx, newObj)
}

func (v *SpokeClusterValidator) ValidateDelete(_ context.Context, _ *hubv1alpha1.SpokeCluster) (admission.Warnings, error) {
	return nil, nil
}

func (v *SpokeClusterValidator) validate(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (admission.Warnings, error) {
	var hubConfig hubv1alpha1.HubConfig
	if err := v.client.Get(ctx, client.ObjectKey{Name: "cluster"}, &hubConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrHubConfigNotFound
		}
		return nil, fmt.Errorf("failed to get HubConfig: %w", err)
	}

	switch hubConfig.Spec.ClusterRegistryMode {
	case hubv1alpha1.ClusterRegistryModeSecret:
		if sc.Spec.CredentialSource.Secret == nil {
			return nil, fmt.Errorf("%w: expected 'secret', got 'mce'", ErrCredentialSourceMismatch)
		}
	case hubv1alpha1.ClusterRegistryModeMCE:
		if sc.Spec.CredentialSource.MCE == nil {
			return nil, fmt.Errorf("%w: expected 'mce', got 'secret'", ErrCredentialSourceMismatch)
		}
	default:
		return nil, fmt.Errorf("%w: unknown clusterRegistryMode %q", ErrCredentialSourceMismatch, hubConfig.Spec.ClusterRegistryMode)
	}

	return nil, nil
}
