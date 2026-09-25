#!/usr/bin/env bash
# Creates kind hub + 2 spoke clusters, builds and deploys the hub operator on hub,
# writes env vars to $1 (default /tmp/lightspeed-hub-mc-e2e-env).
#
# Env vars written:
#   MC_HUB_KUBECONFIG               - hub external kubeconfig (for test binary)
#   MC_SPOKE_KUBECONFIGS            - comma-separated spoke external kubeconfigs
#   MC_SPOKE_INTERNAL_KUBECONFIGS   - comma-separated spoke internal kubeconfigs (container-IP)
#   MC_SPOKE_CONTAINER_NAMES        - comma-separated kind control-plane container names
set -euo pipefail

ENV_FILE="${1:-/tmp/lightspeed-hub-mc-e2e-env}"
IMG="${IMG:-lightspeed-hub-operator:e2e}"
OPERATOR_NS="openshift-lightspeed"
DEPLOY_NAME="lightspeed-hub-controller-manager"
HEALTH_INTERVAL="15s"

HUB=lightspeed-hub
SPOKE1=lightspeed-spoke1
SPOKE2=lightspeed-spoke2

# Prerequisites
command -v kind    >/dev/null 2>&1 || { echo "ERROR: kind not found in PATH"; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH"; exit 1; }
python3 --version  >/dev/null 2>&1 || { echo "ERROR: python3 not found in PATH"; exit 1; }

# Detect container tool (docker preferred, podman fallback)
if command -v docker >/dev/null 2>&1; then
  CONTAINER_TOOL=docker
elif command -v podman >/dev/null 2>&1; then
  CONTAINER_TOOL=podman
  # kind needs this env var to use podman as its container runtime
  export KIND_EXPERIMENTAL_PROVIDER=podman
else
  echo "ERROR: neither docker nor podman found in PATH"; exit 1
fi
echo "==> Using container tool: $CONTAINER_TOOL"

TMPDIR=$(mktemp -d)

HUB_KC=$TMPDIR/hub.kubeconfig
SPOKE1_EXT=$TMPDIR/spoke1-ext.kubeconfig
SPOKE2_EXT=$TMPDIR/spoke2-ext.kubeconfig
SPOKE1_INT=$TMPDIR/spoke1-int.kubeconfig
SPOKE2_INT=$TMPDIR/spoke2-int.kubeconfig

# Create clusters
echo "==> Creating kind clusters..."
kind create cluster --name "$HUB"    --kubeconfig "$HUB_KC"
kind create cluster --name "$SPOKE1" --kubeconfig "$SPOKE1_EXT"
kind create cluster --name "$SPOKE2" --kubeconfig "$SPOKE2_EXT"

# Build operator image and load into hub cluster
echo "==> Building operator image $IMG..."
IMG="$IMG" make docker-build
# podman stores locally-built images with a localhost/ prefix; kind needs the bare name.
# Tag without the prefix so kind load can find it.
if [ "$CONTAINER_TOOL" = "podman" ]; then
  podman tag "localhost/$IMG" "$IMG" 2>/dev/null || true
fi
kind load docker-image "$IMG" --name "$HUB"

# Deploy operator on hub (installs CRDs and operator Deployment via kustomize)
echo "==> Installing CRDs and deploying operator on hub..."
KUBECONFIG="$HUB_KC" make install
IMG="$IMG" KUBECONFIG="$HUB_KC" make deploy

# Patch health-check-interval to short value for fast e2e polling
echo "==> Patching health-check-interval to $HEALTH_INTERVAL..."
KUBECONFIG="$HUB_KC" kubectl patch deployment "$DEPLOY_NAME" \
  -n "$OPERATOR_NS" \
  --type=json \
  -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"--health-check-interval=$HEALTH_INTERVAL\"}]"

KUBECONFIG="$HUB_KC" kubectl rollout status deployment/"$DEPLOY_NAME" \
  -n "$OPERATOR_NS" --timeout=90s

INTERVAL_FOUND=$(KUBECONFIG="$HUB_KC" kubectl get deployment "$DEPLOY_NAME" \
  -n "$OPERATOR_NS" \
  -o jsonpath='{.spec.template.spec.containers[0].args}' \
  | grep -c "health-check-interval" || true)
[ "$INTERVAL_FOUND" -gt 0 ] || { echo "ERROR: health-check-interval not found in deployment args after patch"; exit 1; }
echo "health-check-interval patch verified."

# Build internal kubeconfigs using container network IPs
# insecure-skip-tls-verify avoids SAN matching issues with container IPs
make_internal_kubeconfig() {
  local ext_kc="$1"
  local container="$2"
  local out="$3"
  local ip
  ip=$($CONTAINER_TOOL inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container")
  [ -n "$ip" ] || { echo "ERROR: could not get IP for container $container"; exit 1; }
  python3 - "$ext_kc" "$ip" "$out" <<'PYEOF'
import sys, yaml
ext_kc, ip, out = sys.argv[1], sys.argv[2], sys.argv[3]
with open(ext_kc) as f:
    cfg = yaml.safe_load(f.read())
for c in cfg.get('clusters', []):
    c['cluster']['server'] = f'https://{ip}:6443'
    c['cluster']['insecure-skip-tls-verify'] = True
    c['cluster'].pop('certificate-authority-data', None)
with open(out, 'w') as f:
    yaml.dump(cfg, f)
PYEOF
}

echo "==> Building internal kubeconfigs..."
make_internal_kubeconfig "$SPOKE1_EXT" "${SPOKE1}-control-plane" "$SPOKE1_INT"
make_internal_kubeconfig "$SPOKE2_EXT" "${SPOKE2}-control-plane" "$SPOKE2_INT"

# Write env file
cat > "$ENV_FILE" <<EOF
export MC_HUB_KUBECONFIG=$HUB_KC
export MC_SPOKE_KUBECONFIGS=$SPOKE1_EXT,$SPOKE2_EXT
export MC_SPOKE_INTERNAL_KUBECONFIGS=$SPOKE1_INT,$SPOKE2_INT
export MC_SPOKE_CONTAINER_NAMES=${SPOKE1}-control-plane,${SPOKE2}-control-plane
EOF

echo "==> mc-kind-up: done. Env written to $ENV_FILE"
cat "$ENV_FILE"
