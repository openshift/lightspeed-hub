# OLS-3947: SpokeCluster Controller Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the SpokeCluster controller with credential broker (secret mode), spoke provisioning, standing kubeconfig management, health monitoring, and decommission with finalizers.

**Architecture:** Four components: credential broker (interface + secret mode impl + standing kubeconfig builder), spoke provisioner (idempotent create/delete of spoke-side RBAC), SpokeCluster controller (reconciler with finalizer, health checks, status conditions), and wiring in main.go. The controller requeues every 5 min for health checks (30s when unhealthy). Decommission uses a finalizer for best-effort spoke cleanup.

**Tech Stack:** Go, controller-runtime v0.24.1, client-go (clientcmd, discovery), envtest, fake client

**Spec:** `docs/superpowers/specs/2026-09-01-spokecluster-controller-design.md`, `.ai/spec/what/spoke-lifecycle.md` (rules 1-23)

## Global Constraints

- API group: `hub.openshift.io/v1alpha1`
- Module path: `github.com/openshift/lightspeed-hub`
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Idempotent: Create + handle AlreadyExists (not Get-then-Create)
- License header: `hack/boilerplate.go.txt` (replace YEAR with 2026)
- Commit messages: start with `OLS-3947`
- Spoke managed namespace: `openshift-lightspeed-managed` (NOT `openshift-lightspeed`)
- Finalizer: `hub.openshift.io/spoke-cleanup`
- Standing kubeconfig name: `spoke-kubeconfig-{SpokeCluster.metadata.name}`
- Health check: 5 min requeue (healthy), 30s requeue (unhealthy)
- Spoke dial timeout: 10 seconds

---

### Task 1: Credential Broker + Standing Kubeconfig

**Files:**
- Create: `internal/credential/broker.go`
- Create: `internal/credential/secret.go`
- Create: `internal/credential/kubeconfig.go`
- Create: `internal/credential/secret_test.go`
- Create: `internal/credential/kubeconfig_test.go`

**Interfaces:**
- Consumes: `hubv1alpha1.SpokeCluster` (Spec.CredentialSource.Secret), `corev1.Secret` (admin kubeconfig), `client.Reader` (hub client)
- Produces: `CredentialSource` interface (`GetRESTConfig(ctx, *SpokeCluster) (*rest.Config, error)`), `NewSecretCredentialSource(client.Reader) *SecretCredentialSource`, `BuildStandingKubeconfig(cfg *rest.Config, sc *SpokeCluster, namespace string) (*corev1.Secret, error)`, `StandingKubeconfigName(spokeName string) string`, `KubeconfigKey` constant

- [ ] **Step 1: Create `internal/credential/broker.go`**

```go
package credential

import (
	"context"

	"k8s.io/client-go/rest"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

type CredentialSource interface {
	GetRESTConfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error)
}
```

- [ ] **Step 2: Create `internal/credential/secret.go`**

```go
package credential

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
)

type SecretCredentialSource struct {
	hubClient client.Reader
}

func NewSecretCredentialSource(hubClient client.Reader) *SecretCredentialSource {
	return &SecretCredentialSource{hubClient: hubClient}
}

func (s *SecretCredentialSource) GetRESTConfig(ctx context.Context, sc *hubv1alpha1.SpokeCluster) (*rest.Config, error) {
	if sc.Spec.CredentialSource.Secret == nil {
		return nil, fmt.Errorf("spoke %q has no secret credential source", sc.Name)
	}

	ref := sc.Spec.CredentialSource.Secret
	var secret corev1.Secret
	key := client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}
	if err := s.hubClient.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("reading admin kubeconfig secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	kubeconfig, ok := secret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("admin kubeconfig secret %s/%s missing 'kubeconfig' key", ref.Namespace, ref.Name)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig from secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	return cfg, nil
}
```

- [ ] **Step 3: Create `internal/credential/kubeconfig.go`**

```go
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
	cluster.CertificateAuthorityData = cfg.TLSClientConfig.CAData
	if cluster.Server == "" {
		cluster.Server = cfg.Host
	}

	user := clientcmdapi.NewAuthInfo()
	if cfg.BearerToken != "" {
		user.Token = cfg.BearerToken
	}
	if len(cfg.TLSClientConfig.CertData) > 0 {
		user.ClientCertificateData = cfg.TLSClientConfig.CertData
		user.ClientKeyData = cfg.TLSClientConfig.KeyData
	}

	return clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"spoke": cluster},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"spoke-user": user},
		Contexts:       map[string]*clientcmdapi.Context{"spoke": {Cluster: "spoke", AuthInfo: "spoke-user"}},
		CurrentContext: "spoke",
	}
}
```

- [ ] **Step 4: Create `internal/credential/secret_test.go`**

Tests: valid kubeconfig → rest.Config, secret not found → error, missing kubeconfig key → error, nil secret credential source → error. Uses fake client with corev1 scheme.

- [ ] **Step 5: Create `internal/credential/kubeconfig_test.go`**

Tests: token auth → valid kubeconfig YAML, client cert auth → valid kubeconfig YAML, owner reference set correctly, StandingKubeconfigName format. Round-trips through `clientcmd.RESTConfigFromKubeConfig` to verify the generated kubeconfig is parseable.

- [ ] **Step 6: Run tests and verify**

```bash
go test ./internal/credential/ -v -count=1
```

- [ ] **Step 7: Commit**

```bash
git add internal/credential/
git commit -m "OLS-3947 Add credential broker and standing kubeconfig builder"
```

---

### Task 2: Spoke Provisioner

**Files:**
- Create: `internal/provisioner/spoke.go`
- Create: `internal/provisioner/spoke_test.go`

**Interfaces:**
- Consumes: `client.Client` (spoke cluster client)
- Produces: `Provision(ctx context.Context, spokeClient client.Client) error`, `Deprovision(ctx context.Context, spokeClient client.Client, log logr.Logger)`

- [ ] **Step 1: Create `internal/provisioner/spoke.go`**

Creates 4 resources on spoke (Namespace `openshift-lightspeed-managed`, ServiceAccount `lightspeed-agent`, ClusterRoleBindings `lightspeed-hub:cluster-reader` and `lightspeed-hub:cluster-monitoring-view`). Idempotent via Create + AlreadyExists. Deprovision deletes in reverse order, best-effort (errors logged via logr.Logger, not returned).

- [ ] **Step 2: Create `internal/provisioner/spoke_test.go`**

Tests: provision creates 4 resources, provision idempotent when all exist, provision idempotent when partial exist, deprovision deletes all, deprovision best-effort when resources don't exist, deprovision best-effort when only some exist.

- [ ] **Step 3: Run tests and verify**

```bash
go test ./internal/provisioner/ -v -count=1
```

- [ ] **Step 4: Commit**

```bash
git add internal/provisioner/
git commit -m "OLS-3947 Add spoke provisioner with idempotent create and best-effort cleanup"
```

---

### Task 3: SpokeCluster Controller + Wiring

**Files:**
- Create: `internal/controller/spokecluster_controller.go`
- Create: `internal/controller/spokecluster_controller_test.go`
- Modify: `cmd/main.go`
- Modify: `PROJECT`

**Interfaces:**
- Consumes: `credential.CredentialSource` (Task 1), `credential.BuildStandingKubeconfig` (Task 1), `credential.StandingKubeconfigName` (Task 1), `credential.KubeconfigKey` (Task 1), `provisioner.Provision` (Task 2), `provisioner.Deprovision` (Task 2)
- Produces: `SpokeClusterReconciler` struct, `NewSpokeClusterReconciler(client, credSource, namespace) *SpokeClusterReconciler`, `SetupWithManager(mgr) error`

- [ ] **Step 1: Create `internal/controller/spokecluster_controller.go`**

Reconciler with:
- Finalizer management (`hub.openshift.io/spoke-cleanup`)
- Credential resolution via injected `CredentialSource`
- Standing kubeconfig create/update (Create + AlreadyExists + Update)
- Connectivity check via injected `CheckConnectivity func(*rest.Config) error` (default: `discovery.ServerVersion()`)
- Spoke provisioning via `provisioner.Provision`
- Status condition `Connected` (True/False with reasons)
- Requeue: 5 min healthy, 30s unhealthy
- Delete path: read standing kubeconfig, deprovision spoke (best-effort), remove finalizer
- Injected `NewSpokeClient func(*rest.Config) (client.Client, error)` for testability
- RBAC markers: spokeclusters (get/list/watch/update/patch), spokeclusters/status, spokeclusters/finalizers, secrets (get/list/watch/create/update/delete), events (create/patch)

- [ ] **Step 2: Create `internal/controller/spokecluster_controller_test.go`**

Uses `package controller` (internal tests, accesses unexported constants). Tests with fake hub client + mocked spoke dependencies:
- `TestReconcile_AddsFinalizer`: new SpokeCluster → finalizer added, requeue
- `TestReconcile_HappyPath`: SpokeCluster with finalizer → Connected=True, standing kubeconfig exists, requeue 5m
- `TestReconcile_CredentialError`: credential source returns error → Connected=False (CredentialError), requeue 30s
- `TestReconcile_ConnectivityFailure`: connectivity check fails → Connected=False (ConnectionFailed), requeue 30s
- `TestReconcile_DeleteWithFinalizer`: deleting SpokeCluster with standing kubeconfig → deprovision called, finalizer removed
- `TestReconcile_DeleteWhenSpokeUnreachable`: spoke unreachable during delete → finalizer still removed (best-effort)
- `TestReconcile_DeleteNoStandingKubeconfig`: no standing kubeconfig → finalizer removed without error
- `TestReconcile_NotFound`: SpokeCluster doesn't exist → no error, no requeue
- `TestReconcile_IdempotentConverges`: reconcile twice → same result, Connected=True

Key test infrastructure:
- `fakeCredentialSource` implementing `CredentialSource` with configurable cfg/err
- `fakeSpokeClientFactory` returning a fake spoke client
- `successfulConnectivity` / `failingConnectivity` functions
- `newSpokeClusterWithFinalizer` / `newSpokeClusterDeleting` helpers
- `WithStatusSubresource(&hubv1alpha1.SpokeCluster{})` on fake client builder

- [ ] **Step 3: Run tests to verify**

```bash
go test ./internal/controller/ -v -count=1
```

- [ ] **Step 4: Wire controller in `cmd/main.go`**

Add imports `internal/controller` and `internal/credential`. Before `// +kubebuilder:scaffold:builder`, add:
```go
credSource := credential.NewSecretCredentialSource(mgr.GetClient())
if err := controller.NewSpokeClusterReconciler(
    mgr.GetClient(),
    credSource,
    namespace,
).SetupWithManager(mgr); err != nil {
    log.Error(err, "unable to create controller", "controller", "SpokeCluster")
    os.Exit(1)
}
```

- [ ] **Step 5: Update `PROJECT` file**

Change SpokeCluster entry: `controller: false` → `controller: true`

- [ ] **Step 6: Run `make generate manifests` to regenerate RBAC**

Verify `config/rbac/role.yaml` includes: spokeclusters, spokeclusters/status, spokeclusters/finalizers, secrets, events

- [ ] **Step 7: Run full test suite and build**

```bash
make build
make test
make lint
```

- [ ] **Step 8: Commit**

```bash
git add internal/controller/spokecluster_controller.go internal/controller/spokecluster_controller_test.go cmd/main.go PROJECT config/rbac/
git commit -m "OLS-3947 Add SpokeCluster controller with health check and finalizer"
```

---

## Verification

1. `make build` — compiles
2. `make test` — all tests pass (credential, provisioner, controller, existing CEL tests)
3. `make lint` — no warnings
4. `make manifests` — RBAC role.yaml updated with controller permissions
5. Inspect `config/rbac/role.yaml` for spokeclusters, secrets, events permissions
