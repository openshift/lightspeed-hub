# End-to-End Testing on OpenShift

Manual e2e test for the hub operator on a live OCP cluster. Uses a single cluster acting as both hub and spoke.

## Prerequisites

- Access to an OCP cluster (`oc login` or `KUBECONFIG` set)
- `kubectl` / `oc` CLI
- Operator image pushed to a public registry (e.g. `quay.io/hasun/lightspeed-hub-operator:latest`)

## Build and Push

```bash
IMG=quay.io/hasun/lightspeed-hub-operator:latest
podman build -t $IMG .
podman push $IMG
```

## Deploy

```bash
# Install CRDs
make install

# Deploy the operator (uses kustomize)
IMG=quay.io/hasun/lightspeed-hub-operator:latest make deploy

# Verify operator is running
kubectl get pods -n openshift-lightspeed -l control-plane=controller-manager
kubectl logs -n openshift-lightspeed -l control-plane=controller-manager --tail=10
```

## Test Registration

```bash
# 1. Create HubConfig (secret mode)
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: secret
EOF

# 2. Create admin kubeconfig Secret (self-referencing — points at same cluster)
kubectl create secret generic self-spoke-creds \
  -n openshift-lightspeed \
  --from-file=kubeconfig=$KUBECONFIG

# 3. Create SpokeCluster
API_SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
kubectl apply -f - <<EOF
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: self-spoke
spec:
  apiServer: "$API_SERVER"
  credentialSource:
    secret:
      name: self-spoke-creds
      namespace: openshift-lightspeed
EOF
```

## Verify Registration

```bash
# SpokeCluster status — expect Connected: True (ConnectionSucceeded)
kubectl get spokecluster self-spoke -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}) - {.message}{"\n"}{end}'

# Standing kubeconfig Secret created on hub
kubectl get secret spoke-kubeconfig-self-spoke -n openshift-lightspeed

# Spoke-side resources created
kubectl get ns openshift-lightspeed-managed
kubectl get sa lightspeed-agent -n openshift-lightspeed-managed
kubectl get clusterrolebinding lightspeed-hub:cluster-reader
kubectl get clusterrolebinding lightspeed-hub:cluster-monitoring-view
```

## Verify Standing Kubeconfig

Extract the standing kubeconfig and verify it can access the spoke cluster:

```bash
# Extract the standing kubeconfig from the Secret
kubectl get secret spoke-kubeconfig-self-spoke -n openshift-lightspeed \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > /tmp/spoke-kubeconfig

# Test basic access via the standing kubeconfig
KUBECONFIG=/tmp/spoke-kubeconfig kubectl get nodes
KUBECONFIG=/tmp/spoke-kubeconfig kubectl get namespaces
KUBECONFIG=/tmp/spoke-kubeconfig kubectl get pods -A --no-headers | head -10

# Cleanup
rm /tmp/spoke-kubeconfig
```

Note: In a self-referencing test (single cluster), the standing kubeconfig contains
admin credentials. The reader RBAC is for the `lightspeed-agent` SA, not for the
standing kubeconfig itself.

## Verify Spoke Reader RBAC

Verify the `lightspeed-agent` SA has the expected read-only permissions:

```bash
# Should be allowed (cluster-reader)
kubectl auth can-i get nodes \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: yes

kubectl auth can-i list pods --all-namespaces \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: yes

# Should be allowed (cluster-monitoring-view)
kubectl auth can-i get prometheusrules --all-namespaces \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: yes

# Should be denied (no write access)
kubectl auth can-i create deployments \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: no

kubectl auth can-i delete pods \
  --as=system:serviceaccount:openshift-lightspeed-managed:lightspeed-agent
# Expected: no
```

## Verify Health Check

Wait 5+ minutes and check that the controller re-reconciles:

```bash
kubectl logs -n openshift-lightspeed -l control-plane=controller-manager --tail=10
# Expect periodic "Successfully reconciled spoke" log entries
```

## Test Decommission

```bash
# Delete the SpokeCluster
kubectl delete spokecluster self-spoke

# Verify spoke-side cleanup
kubectl get ns openshift-lightspeed-managed                    # Terminating or NotFound
kubectl get sa lightspeed-agent -n openshift-lightspeed-managed  # NotFound
kubectl get clusterrolebinding lightspeed-hub:cluster-reader     # NotFound
kubectl get clusterrolebinding lightspeed-hub:cluster-monitoring-view  # NotFound

# Verify standing kubeconfig GC'd via owner reference
kubectl get secret spoke-kubeconfig-self-spoke -n openshift-lightspeed  # NotFound

# Check operator logs for decommission
kubectl logs -n openshift-lightspeed -l control-plane=controller-manager --tail=5
# Expect: "Deprovisioning spoke" and "Removed finalizer, spoke cleanup complete"
```

## Test CEL Validation

These work without the operator running — enforced by the API server:

```bash
# Reject HubConfig with wrong name
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: not-cluster
spec:
  clusterRegistryMode: secret
EOF
# Expected error: HubConfig name must be 'cluster'

# Reject invalid clusterRegistryMode
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: invalid
EOF
# Expected error: supported values: "secret", "mce"

# Reject SpokeCluster with both credential sources
kubectl apply -f - <<'EOF'
apiVersion: hub.openshift.io/v1alpha1
kind: SpokeCluster
metadata:
  name: bad-spoke
spec:
  apiServer: "https://api.example.com:6443"
  credentialSource:
    secret:
      name: creds
      namespace: openshift-lightspeed
    mce:
      managedClusterName: bad-spoke
EOF
# Expected error: exactly one of secret or mce must be specified
```

## Cleanup

```bash
kubectl delete hubconfig cluster
make undeploy   # removes operator deployment, RBAC, CRDs
```
