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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/credential"
)

type SpokeHealthHandler struct {
	client            client.Client
	operatorNamespace string
	checkInterval     time.Duration
	CheckConnectivity func(cfg *rest.Config) error
}

func NewSpokeHealthHandler(c client.Client, ns string, interval time.Duration) *SpokeHealthHandler {
	return &SpokeHealthHandler{
		client:            c,
		operatorNamespace: ns,
		checkInterval:     interval,
		CheckConnectivity: defaultCheckConnectivity,
	}
}

func (h *SpokeHealthHandler) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("spoke-health")
	logger.Info("Starting spoke health handler", "interval", h.checkInterval)
	h.CheckAll(ctx)
	ticker := time.NewTicker(h.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			h.CheckAll(ctx)
		}
	}
}

func (h *SpokeHealthHandler) CheckAll(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("spoke-health")

	var spokeList hubv1alpha1.SpokeClusterList
	if err := h.client.List(ctx, &spokeList); err != nil {
		logger.Error(err, "Failed to list SpokeCluster CRs")
		return
	}

	for i := range spokeList.Items {
		h.checkSpoke(ctx, &spokeList.Items[i])
	}
}

func (h *SpokeHealthHandler) checkSpoke(ctx context.Context, sc *hubv1alpha1.SpokeCluster) {
	logger := log.FromContext(ctx).WithName("spoke-health").WithValues("spoke", sc.Name)

	secretKey := client.ObjectKey{
		Name:      credential.StandingKubeconfigName(sc.Name),
		Namespace: h.operatorNamespace,
	}
	var secret corev1.Secret
	if err := h.client.Get(ctx, secretKey, &secret); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to read standing kubeconfig")
		}
		return
	}

	kubeconfigBytes, ok := secret.Data[credential.KubeconfigKey]
	if !ok {
		return
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		logger.Error(err, "Failed to parse standing kubeconfig")
		return
	}
	cfg.Timeout = spokeDialTimeout

	connectErr := h.CheckConnectivity(cfg)

	var newStatus metav1.ConditionStatus
	var reason, message string
	if connectErr != nil {
		newStatus = metav1.ConditionFalse
		reason = reasonConnectionFailed
		message = connectErr.Error()
	} else {
		newStatus = metav1.ConditionTrue
		reason = reasonConnectionSucceeded
		message = "spoke API server is reachable"
	}

	existing := meta.FindStatusCondition(sc.Status.Conditions, conditionTypeConnected)
	if existing != nil && existing.Status == newStatus {
		return
	}

	base := sc.DeepCopy()
	meta.SetStatusCondition(&sc.Status.Conditions, metav1.Condition{
		Type:               conditionTypeConnected,
		Status:             newStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: sc.Generation,
	})
	if err := h.client.Status().Patch(ctx, sc, client.MergeFrom(base)); err != nil {
		logger.Error(err, "Failed to patch Connected condition")
		return
	}
	logger.Info("Updated Connected condition", "status", newStatus)
}
