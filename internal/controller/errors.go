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

import "errors"

var (
	// Lifecycle
	ErrAddingFinalizer   = errors.New("adding finalizer")
	ErrRemovingFinalizer = errors.New("removing finalizer")
	ErrUpdatingStatus    = errors.New("updating status")

	// HubConfig
	ErrGettingHubConfig = errors.New("getting HubConfig")

	// Connectivity
	ErrCreatingDiscoveryClient = errors.New("creating discovery client")
	ErrCheckingServerVersion   = errors.New("checking server version")
	ErrCreatingSpokeClient     = errors.New("creating spoke client")

	// Standing kubeconfig
	ErrBuildingStandingKubeconfig   = errors.New("building standing kubeconfig")
	ErrCreatingStandingKubeconfig   = errors.New("creating standing kubeconfig")
	ErrGettingStandingKubeconfig    = errors.New("getting existing standing kubeconfig")
	ErrUpdatingStandingKubeconfig   = errors.New("updating standing kubeconfig")
	ErrReadingStandingKubeconfig    = errors.New("reading standing kubeconfig")
	ErrMissingStandingKubeconfigKey = errors.New("standing kubeconfig secret missing key")
	ErrParsingStandingKubeconfig    = errors.New("parsing standing kubeconfig")

	// Adapter orchestration (reconcileAdapters)
	ErrProvisioningAdapterResources = errors.New("provisioning adapter resources")
	ErrRequestingAdapterToken       = errors.New("requesting adapter SA token")
	ErrDiscoveringAlertmanagerURL   = errors.New("discovering AlertManager URL")
	ErrEnsuringAdapterCredential    = errors.New("ensuring adapter credential Secret")
	ErrLabelingSpokeCluster         = errors.New("labeling SpokeCluster")
	ErrRestartingAdapter            = errors.New("restarting adapter")

	// Adapter credential Secret (ensureAdapterCredentialSecret)
	ErrCreatingAdapterCredential = errors.New("creating adapter credential Secret")
	ErrGettingAdapterCredential  = errors.New("getting existing adapter credential Secret")
	ErrUpdatingAdapterCredential = errors.New("updating adapter credential Secret")

	// SpokeCluster label update (ensureAdapterLabel)
	ErrGettingSpokeCluster  = errors.New("getting SpokeCluster for label update")
	ErrUpdatingSpokeCluster = errors.New("updating SpokeCluster labels")

	// AlertManager route (defaultDiscoverAlertmanagerURL)
	ErrGettingAlertmanagerRoute = errors.New("getting alertmanager-main Route")
	ErrMissingAlertmanagerHost  = errors.New("alertmanager-main Route missing spec.host")

	// Adapter stack (hubconfig_controller.go)
	ErrTearingDownAdapterStack = errors.New("tearing down adapter stack")
	ErrEnsuringAdapterStack    = errors.New("ensuring adapter stack")

	// Adapter resource CRUD (adapter_deployment.go)
	ErrCreatingAdapterResource   = errors.New("creating adapter resource")
	ErrGettingAdapterResource    = errors.New("getting existing adapter resource")
	ErrUpdatingAdapterResource   = errors.New("updating adapter resource")
	ErrDeletingAdapterResource   = errors.New("deleting adapter resource")
	ErrCreatingAdapterDeployment = errors.New("creating Deployment")
	ErrGettingAdapterDeployment  = errors.New("getting existing Deployment")
	ErrUpdatingAdapterDeployment = errors.New("updating Deployment")
	ErrGettingAdapterForRestart  = errors.New("getting adapter Deployment for restart")
	ErrUpdatingAdapterForRestart = errors.New("updating adapter Deployment for restart")
)
