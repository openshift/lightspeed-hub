#!/usr/bin/env bash
# Prepares a fresh OpenShift 5 cluster for the alert pipeline e2e test
# (make alert-pipeline-e2e). The cluster acts as both hub and spoke, with ACM as
# the cluster inventory:
#
#   1. ACM with hub self-management disabled (local-cluster is always excluded by
#      hub discovery), Basic availability and the ACM components the test does not
#      use turned off (observability, search, grc, insights, submariner, volsync,
#      console, app-lifecycle), so it fits a small cluster. MCE stays enabled.
#   2. The hub imported again as an ordinary ManagedCluster "hub-self".
#   3. lightspeed-agentic-operator, then the lightspeed-hub controller.
#   4. HubConfig in mce mode -> discovered SpokeCluster/hub-self.
#   5. LLM provider, Agent "default" and an all-Automatic ApprovalPolicy.
#   6. Unsilenced critical alerts are silenced (and listed) before the adapter is
#      enabled, so auto-approved runs never act on real cluster problems.
#   7. Alerts adapter opt-in (allowedReceivers: [Critical]) and restart.
#
# Every step is idempotent: re-running converges and skips completed work.
# See e2e-test.md, "Alert Pipeline E2E Suite", and
# .ai/spec/what/multicluster-testing.md, "Alert pipeline E2E".
#
# Required env:
#   KUBECONFIG              cluster-admin kubeconfig with a static token or client
#                           certificate (also used to auto-import the hub)
#   HUB_IMAGE               lightspeed-hub controller image
#   OPENAI_API_KEY          LLM API key (not needed with SKIP_LLM_SETUP=true)
#
# Optional env:
#   LLM_MODEL               Agent/default model (default: gpt-6.1-sol)
#   SKIP_LLM_SETUP          "true" skips Secret/LLMProvider/Agent (Phase 1 only runs)
#   AGENTIC_OPERATOR_IMAGE  default: Konflux lightspeed-agentic-operator:main
#   SANDBOX_IMAGE           default: Konflux lightspeed-agentic-sandbox:main
#   ALERTS_ADAPTER_IMAGE    default: Konflux lightspeed-agentic-alerts-adapter:main
#   AGENTIC_OPERATOR_REPO   default: https://github.com/openshift/lightspeed-agentic-operator.git
#   AGENTIC_OPERATOR_REF    git branch, tag or commit whose manifests are deployed (default: main)
#   ACM_CHANNEL             ACM subscription channel (default: package default channel)
#   ACM_AVAILABILITY        MultiClusterHub availabilityConfig (default: Basic)
#   ACM_DISABLED_COMPONENTS space-separated MultiClusterHub components to disable (default:
#                           multicluster-observability search grc insights submariner-addon
#                           volsync console app-lifecycle)
#   SILENCE_DURATION_HOURS  duration of silences for pre-existing critical alerts (default: 24)
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
KONFLUX_REGISTRY="quay.io/redhat-user-workloads/crt-nshift-lightspeed-tenant"

HUB_IMAGE="${HUB_IMAGE:-}"
OPENAI_API_KEY="${OPENAI_API_KEY:-}"
LLM_MODEL="${LLM_MODEL:-gpt-6.1-sol}"
SKIP_LLM_SETUP="${SKIP_LLM_SETUP:-false}"
AGENTIC_OPERATOR_IMAGE="${AGENTIC_OPERATOR_IMAGE:-${KONFLUX_REGISTRY}/lightspeed-agentic-operator:main}"
SANDBOX_IMAGE="${SANDBOX_IMAGE:-${KONFLUX_REGISTRY}/lightspeed-agentic-sandbox:main}"
ALERTS_ADAPTER_IMAGE="${ALERTS_ADAPTER_IMAGE:-${KONFLUX_REGISTRY}/lightspeed-agentic-alerts-adapter:main}"
AGENTIC_OPERATOR_REPO="${AGENTIC_OPERATOR_REPO:-https://github.com/openshift/lightspeed-agentic-operator.git}"
AGENTIC_OPERATOR_REF="${AGENTIC_OPERATOR_REF:-main}"
ACM_CHANNEL="${ACM_CHANNEL:-}"
ACM_AVAILABILITY="${ACM_AVAILABILITY:-Basic}"
ACM_DISABLED_COMPONENTS="${ACM_DISABLED_COMPONENTS:-multicluster-observability search grc insights submariner-addon volsync console app-lifecycle}"
SILENCE_DURATION_HOURS="${SILENCE_DURATION_HOURS:-24}"

OPERATOR_NS="openshift-lightspeed"
ACM_NS="open-cluster-management"
ACM_PACKAGE="advanced-cluster-management"
MCH_NAME="multiclusterhub"
SPOKE_NAME="hub-self"
HUB_DEPLOYMENT="lightspeed-hub-controller-manager"
HUB_CONTROL_PLANE_LABEL="hub-controller-manager"
AGENTIC_DEPLOYMENT="controller-manager"
ADAPTER_DEPLOYMENT="lightspeed-hub-alerts-adapter"
ADAPTER_CONFIGMAP="hub-alerts-adapter-config"
# Alert raised by the e2e test itself; never silenced.
TEST_ALERT_NAME="LightspeedE2EDeploymentUnavailable"
SPOKE_ADDONS=(cluster-proxy managed-serviceaccount work-manager)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d)"
PF_PID=""

cleanup() {
  if [[ -n "${PF_PID}" ]]; then kill "${PF_PID}" 2>/dev/null || true; fi
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
die()  { printf '\nERROR: %s\n' "$*" >&2; exit 1; }

# wait_for <description> <timeout-seconds> <command...>: retries the command every 10s.
wait_for() {
  local desc="$1" timeout="$2"; shift 2
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      die "timed out after ${timeout}s waiting for: ${desc}"
    fi
    sleep 10
  done
  info "ready: ${desc}"
}

exists() { oc get "$@" >/dev/null 2>&1; }

# condition_true <resource> <name> <type> [namespace]
condition_true() {
  local ns_args=()
  [[ -n "${4:-}" ]] && ns_args=(-n "$4")
  [[ "$(oc get "$1" "$2" "${ns_args[@]}" -o jsonpath="{.status.conditions[?(@.type==\"$3\")].status}" 2>/dev/null)" == "True" ]]
}

# ---------------------------------------------------------------------------
# 1. Preflight
# ---------------------------------------------------------------------------
preflight() {
  log "Preflight checks"
  local tool
  for tool in oc curl git make go; do
    command -v "${tool}" >/dev/null 2>&1 || die "required tool not found in PATH: ${tool}"
  done
  [[ -n "${KUBECONFIG:-}" && -f "${KUBECONFIG}" ]] || die "KUBECONFIG must point to a cluster-admin kubeconfig file"
  [[ -n "${HUB_IMAGE}" ]] || die "HUB_IMAGE must be set to the lightspeed-hub controller image"
  if [[ "${SKIP_LLM_SETUP}" != "true" && -z "${OPENAI_API_KEY}" ]]; then
    die "OPENAI_API_KEY must be set (or SKIP_LLM_SETUP=true for Phase 1 only)"
  fi

  oc whoami >/dev/null || die "cannot reach the cluster with KUBECONFIG=${KUBECONFIG}"
  local version
  version="$(oc get clusterversion version -o jsonpath='{.status.desired.version}')"
  [[ "${version}" == 5.* ]] || die "OpenShift 5.x required, cluster reports ${version}"
  if [[ -n "$(oc config view --minify --raw -o jsonpath='{.users[0].user.exec}')" ]]; then
    die "KUBECONFIG uses exec-based auth; a static token or client certificate is required for the hub auto-import"
  fi
  info "cluster: $(oc whoami --show-server), OpenShift ${version}, user $(oc whoami)"
  info "hub image: ${HUB_IMAGE}"
}

# ---------------------------------------------------------------------------
# 2. ACM
# ---------------------------------------------------------------------------
install_acm_operator() {
  log "Installing ACM operator"
  exists namespace "${ACM_NS}" || oc create namespace "${ACM_NS}" >/dev/null

  if [[ -z "$(oc get operatorgroup -n "${ACM_NS}" -o name 2>/dev/null)" ]]; then
    oc apply -f - >/dev/null <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: ${ACM_NS}
  namespace: ${ACM_NS}
spec:
  targetNamespaces:
  - ${ACM_NS}
EOF
    info "created OperatorGroup"
  fi

  if ! exists subscription.operators.coreos.com "${ACM_PACKAGE}" -n "${ACM_NS}"; then
    local source channel
    source="$(oc get packagemanifest "${ACM_PACKAGE}" -n openshift-marketplace -o jsonpath='{.status.catalogSource}')" \
      || die "package ${ACM_PACKAGE} not found in openshift-marketplace"
    channel="${ACM_CHANNEL:-$(oc get packagemanifest "${ACM_PACKAGE}" -n openshift-marketplace -o jsonpath='{.status.defaultChannel}')}"
    oc apply -f - >/dev/null <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: ${ACM_PACKAGE}
  namespace: ${ACM_NS}
spec:
  name: ${ACM_PACKAGE}
  channel: ${channel}
  source: ${source}
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF
    info "created Subscription (channel ${channel}, source ${source})"
  else
    info "Subscription already exists"
  fi

  wait_for "ACM operator CSV Succeeded" 900 acm_csv_succeeded
  wait_for "MultiClusterHub CRD" 300 oc get crd multiclusterhubs.operator.open-cluster-management.io
}

acm_csv_succeeded() {
  local csv
  csv="$(oc get subscription.operators.coreos.com "${ACM_PACKAGE}" -n "${ACM_NS}" -o jsonpath='{.status.installedCSV}')"
  [[ -n "${csv}" ]] && [[ "$(oc get csv "${csv}" -n "${ACM_NS}" -o jsonpath='{.status.phase}')" == "Succeeded" ]]
}

create_mch() {
  local component components=""
  for component in ${ACM_DISABLED_COMPONENTS}; do
    [[ -n "${components}" ]] && components+=$'\n'
    components+="    - name: ${component}"$'\n'"      enabled: false"
  done
  oc apply -f - <<EOF
apiVersion: operator.open-cluster-management.io/v1
kind: MultiClusterHub
metadata:
  name: ${MCH_NAME}
  namespace: ${ACM_NS}
spec:
  availabilityConfig: ${ACM_AVAILABILITY}
  disableHubSelfManagement: true
  overrides:
    components:
${components}
EOF
}

# disable_mch_component <name>: targeted JSON patch so other component overrides are kept.
disable_mch_component() {
  local name="$1" names i=0 found=-1 n
  names="$(oc get multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" -o jsonpath='{range .spec.overrides.components[*]}{.name}{"\n"}{end}')"
  while IFS= read -r n; do
    [[ -z "${n}" ]] && continue
    if [[ "${n}" == "${name}" ]]; then found=${i}; fi
    i=$((i + 1))
  done <<< "${names}"

  if (( found >= 0 )); then
    oc patch multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" --type json \
      -p "[{\"op\":\"replace\",\"path\":\"/spec/overrides/components/${found}/enabled\",\"value\":false}]" >/dev/null
  else
    if [[ -z "$(oc get multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" -o jsonpath='{.spec.overrides}')" ]]; then
      oc patch multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" --type json \
        -p '[{"op":"add","path":"/spec/overrides","value":{"components":[]}}]' >/dev/null
    elif [[ -z "${names}" ]]; then
      oc patch multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" --type json \
        -p '[{"op":"add","path":"/spec/overrides/components","value":[]}]' >/dev/null
    fi
    oc patch multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" --type json \
      -p "[{\"op\":\"add\",\"path\":\"/spec/overrides/components/-\",\"value\":{\"name\":\"${name}\",\"enabled\":false}}]" >/dev/null
  fi
}

configure_mch() {
  log "Configuring MultiClusterHub (self-management off, ${ACM_AVAILABILITY} availability, disabled: ${ACM_DISABLED_COMPONENTS})"
  if ! exists multiclusterhub "${MCH_NAME}" -n "${ACM_NS}"; then
    # The MultiClusterHub webhook may not be serving right after the CSV succeeds.
    wait_for "MultiClusterHub created" 600 create_mch
  else
    oc patch multiclusterhub "${MCH_NAME}" -n "${ACM_NS}" --type merge \
      -p "{\"spec\":{\"disableHubSelfManagement\":true,\"availabilityConfig\":\"${ACM_AVAILABILITY}\"}}" >/dev/null
    local component
    for component in ${ACM_DISABLED_COMPONENTS}; do
      disable_mch_component "${component}"
    done
    info "patched existing MultiClusterHub"
  fi
  wait_for "MultiClusterHub Running" 1800 \
    bash -c "[[ \"\$(oc get multiclusterhub ${MCH_NAME} -n ${ACM_NS} -o jsonpath='{.status.phase}')\" == Running ]]"
  wait_for "ManagedCluster CRD" 300 oc get crd managedclusters.cluster.open-cluster-management.io
  wait_for "local-cluster removed (hub self-management disabled)" 900 \
    bash -c "! oc get managedcluster local-cluster >/dev/null 2>&1 && ! oc get namespace local-cluster >/dev/null 2>&1"
}

# ---------------------------------------------------------------------------
# 3. Import the hub as ManagedCluster hub-self
# ---------------------------------------------------------------------------
import_hub_self() {
  log "Importing the hub as ManagedCluster ${SPOKE_NAME}"
  exists namespace "${SPOKE_NAME}" || oc create namespace "${SPOKE_NAME}" >/dev/null
  oc apply -f - >/dev/null <<EOF
apiVersion: cluster.open-cluster-management.io/v1
kind: ManagedCluster
metadata:
  name: ${SPOKE_NAME}
  labels:
    cloud: auto-detect
    vendor: auto-detect
spec:
  hubAcceptsClient: true
EOF

  if condition_true managedcluster "${SPOKE_NAME}" ManagedClusterConditionAvailable; then
    info "${SPOKE_NAME} already joined"
  elif ! exists secret auto-import-secret -n "${SPOKE_NAME}"; then
    oc config view --minify --flatten > "${WORK_DIR}/import-kubeconfig"
    oc create secret generic auto-import-secret -n "${SPOKE_NAME}" \
      --from-literal=autoImportRetry=5 \
      --from-file=kubeconfig="${WORK_DIR}/import-kubeconfig" >/dev/null
    rm -f "${WORK_DIR}/import-kubeconfig"
    info "created auto-import-secret"
  fi

  wait_for "ManagedCluster ${SPOKE_NAME} Available" 900 \
    condition_true managedcluster "${SPOKE_NAME}" ManagedClusterConditionAvailable
  local addon
  for addon in "${SPOKE_ADDONS[@]}"; do
    wait_for "addon ${addon} Available on ${SPOKE_NAME}" 900 \
      condition_true managedclusteraddon "${addon}" Available "${SPOKE_NAME}"
  done
  if [[ -n "$(oc get managedcluster "${SPOKE_NAME}" -o jsonpath='{.metadata.labels.local-cluster}')" ]]; then
    die "ManagedCluster ${SPOKE_NAME} carries the local-cluster label; hub discovery would skip it"
  fi
}

# ---------------------------------------------------------------------------
# 4. Agentic operator
# ---------------------------------------------------------------------------
deploy_agentic_operator() {
  log "Deploying lightspeed-agentic-operator (${AGENTIC_OPERATOR_REF})"
  local src="${WORK_DIR}/lightspeed-agentic-operator"
  git init -q "${src}"
  git -C "${src}" fetch -q --depth 1 "${AGENTIC_OPERATOR_REPO}" "${AGENTIC_OPERATOR_REF}"
  git -C "${src}" checkout -q FETCH_HEAD
  info "manifests from $(git -C "${src}" log -1 --format='%h %s')"

  make -C "${src}" deploy IMG="${AGENTIC_OPERATOR_IMAGE}" OPERATOR_NAMESPACE="${OPERATOR_NS}" >/dev/null
  make -C "${src}" deploy-config AGENT_IMAGE="${SANDBOX_IMAGE}" OPERATOR_NAMESPACE="${OPERATOR_NS}" >/dev/null
  oc rollout status "deployment/${AGENTIC_DEPLOYMENT}" -n "${OPERATOR_NS}" --timeout=600s >/dev/null
  info "agentic operator: ${AGENTIC_OPERATOR_IMAGE}"
  info "sandbox: ${SANDBOX_IMAGE}"
}

# ---------------------------------------------------------------------------
# 5. Hub controller
# ---------------------------------------------------------------------------
deploy_hub() {
  log "Deploying lightspeed-hub controller"
  make -C "${REPO_ROOT}" kustomize >/dev/null
  local kustomize="${REPO_ROOT}/bin/kustomize" cfg="${WORK_DIR}/hub-config"
  cp -a "${REPO_ROOT}/config" "${cfg}"
  (cd "${cfg}/manager" && "${kustomize}" edit set image "controller=${HUB_IMAGE}")
  sed -i "s|--alerts-adapter-image=.*|--alerts-adapter-image=${ALERTS_ADAPTER_IMAGE}|" "${cfg}/manager/manager.yaml"

  # spec.selector is immutable: a Deployment from older manifests (generic
  # control-plane=controller-manager label) must be recreated.
  local selector
  selector="$(oc get deployment "${HUB_DEPLOYMENT}" -n "${OPERATOR_NS}" -o jsonpath='{.spec.selector.matchLabels.control-plane}' 2>/dev/null || true)"
  if [[ -n "${selector}" && "${selector}" != "${HUB_CONTROL_PLANE_LABEL}" ]]; then
    info "recreating ${HUB_DEPLOYMENT} (selector control-plane=${selector})"
    oc delete deployment "${HUB_DEPLOYMENT}" -n "${OPERATOR_NS}" --wait=true >/dev/null
  fi

  "${kustomize}" build "${cfg}/default" | oc apply -f - >/dev/null
  oc rollout status "deployment/${HUB_DEPLOYMENT}" -n "${OPERATOR_NS}" --timeout=600s >/dev/null
  info "hub: ${HUB_IMAGE}"
  info "alerts adapter: ${ALERTS_ADAPTER_IMAGE}"
}

# ---------------------------------------------------------------------------
# 6. HubConfig (mce) and discovered SpokeCluster
# ---------------------------------------------------------------------------
configure_hubconfig() {
  log "Configuring HubConfig (mce) and waiting for SpokeCluster/${SPOKE_NAME}"
  local mode
  mode="$(oc get hubconfig cluster -o jsonpath='{.spec.clusterRegistryMode}' 2>/dev/null || true)"
  if [[ -n "${mode}" && "${mode}" != "mce" ]]; then
    die "HubConfig/cluster exists with clusterRegistryMode=${mode}; delete it before running this setup"
  fi
  oc apply -f - >/dev/null <<EOF
apiVersion: hub.openshift.io/v1alpha1
kind: HubConfig
metadata:
  name: cluster
spec:
  clusterRegistryMode: mce
EOF
  wait_for "SpokeCluster/${SPOKE_NAME} discovered" 600 oc get spokecluster "${SPOKE_NAME}"
  local cond
  for cond in Connected Provisioned AdaptersReady Ready; do
    wait_for "SpokeCluster/${SPOKE_NAME} ${cond}=True" 600 condition_true spokecluster "${SPOKE_NAME}" "${cond}"
  done
  wait_for "Deployment ${ADAPTER_DEPLOYMENT}" 300 oc get deployment "${ADAPTER_DEPLOYMENT}" -n "${OPERATOR_NS}"
}

# ---------------------------------------------------------------------------
# 7. LLM and approvals
# ---------------------------------------------------------------------------
configure_llm_and_approvals() {
  if [[ "${SKIP_LLM_SETUP}" == "true" ]]; then
    log "Skipping LLM setup (SKIP_LLM_SETUP=true)"
  else
    log "Configuring LLM provider and Agent/default (model ${LLM_MODEL})"
    local key
    key="$(printf '%s' "${OPENAI_API_KEY}" | tr -d '\r\n')"
    oc create secret generic llm-credentials -n "${OPERATOR_NS}" \
      --from-literal=OPENAI_API_KEY="${key}" --dry-run=client -o yaml | oc apply -f - >/dev/null
    oc apply -f - >/dev/null <<EOF
apiVersion: agentic.openshift.io/v1alpha1
kind: LLMProvider
metadata:
  name: openai
spec:
  type: OpenAI
  openAI:
    credentialsSecret:
      name: llm-credentials
---
apiVersion: agentic.openshift.io/v1alpha1
kind: Agent
metadata:
  name: default
spec:
  llmProvider:
    name: openai
  model: ${LLM_MODEL}
EOF
  fi

  log "Configuring ApprovalPolicy/cluster (all stages Automatic)"
  oc apply -f - >/dev/null <<EOF
apiVersion: agentic.openshift.io/v1alpha1
kind: ApprovalPolicy
metadata:
  name: cluster
spec:
  stages:
  - name: Analysis
    approval: Automatic
  - name: Execution
    approval: Automatic
  - name: Verification
    approval: Automatic
  - name: Escalation
    approval: Automatic
EOF
}

# ---------------------------------------------------------------------------
# 8. Silence pre-existing critical alerts (before the adapter is enabled)
# ---------------------------------------------------------------------------
alertmanager_reachable() { curl -sf -m 5 "http://127.0.0.1:${AM_PORT}/api/v2/status" >/dev/null; }

# write_adapter_config <receivers-yaml-list>: same skills section as the hub's default
# ConfigMap (internal/controller/adapter_deployment.go).
write_adapter_config() {
  cat > "${WORK_DIR}/adapter-config.yaml" <<EOF
filtering:
  allowedReceivers: $1
tools:
  skills:
  - image: quay.io/openshiftanalytics/agentic-skills:latest
    paths:
     - /skills/cluster-troubleshoot/investigate-alert
EOF
  # The hub creates this ConfigMap (create-only) and never overwrites it; replace its content.
  oc create configmap "${ADAPTER_CONFIGMAP}" -n "${OPERATOR_NS}" \
    --from-file=config.yaml="${WORK_DIR}/adapter-config.yaml" --dry-run=client -o yaml | oc replace -f - >/dev/null
}

# restart_adapter: the adapter reads its config and discovers spokes only at startup.
restart_adapter() {
  oc rollout restart "deployment/${ADAPTER_DEPLOYMENT}" -n "${OPERATOR_NS}" >/dev/null
  oc rollout status "deployment/${ADAPTER_DEPLOYMENT}" -n "${OPERATOR_NS}" --timeout=300s >/dev/null
}

# pause_adapter: on a re-run the adapter is already enabled; switch it back to the hub's
# empty allowlist so no run is created between the alert check and the silences.
pause_adapter() {
  if oc get configmap "${ADAPTER_CONFIGMAP}" -n "${OPERATOR_NS}" -o jsonpath='{.data.config\.yaml}' 2>/dev/null \
      | grep -q -i -x '[[:space:]]*-[[:space:]]*critical[[:space:]]*'; then
    write_adapter_config "[]"
    restart_adapter
    info "paused the already-enabled adapter (allowedReceivers: [])"
  fi
}

silence_critical_alerts() {
  log "Silencing pre-existing critical alerts (auto-approved runs must not act on real cluster problems)"
  pause_adapter
  AM_PORT=19093
  oc -n openshift-monitoring port-forward pod/alertmanager-main-0 "${AM_PORT}:9093" >/dev/null 2>&1 &
  PF_PID=$!
  wait_for "AlertManager port-forward" 60 alertmanager_reachable

  local alerts names
  alerts="$(curl -sf -m 10 "http://127.0.0.1:${AM_PORT}/api/v2/alerts?active=true&silenced=false&inhibited=false&filter=severity%3D%22critical%22")"
  names="$(printf '%s' "${alerts}" | grep -o '"alertname":"[^"]*"' | cut -d'"' -f4 | sort -u | grep -v -x "${TEST_ALERT_NAME}" || true)"

  if [[ -z "${names}" ]]; then
    info "no unsilenced critical alerts"
  else
    local start end name id
    start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    end="$(date -u -d "+${SILENCE_DURATION_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ)"
    info "silenced critical alerts (until ${end}):"
    while IFS= read -r name; do
      id="$(curl -sf -m 10 -X POST -H 'Content-Type: application/json' \
        "http://127.0.0.1:${AM_PORT}/api/v2/silences" \
        -d "{\"matchers\":[{\"name\":\"alertname\",\"value\":\"${name}\",\"isRegex\":false,\"isEqual\":true},{\"name\":\"severity\",\"value\":\"critical\",\"isRegex\":false,\"isEqual\":true}],\"startsAt\":\"${start}\",\"endsAt\":\"${end}\",\"createdBy\":\"lightspeed-setup\",\"comment\":\"Silenced by hack/alert-pipeline-setup.sh: keep auto-approved Lightspeed agentic runs away from pre-existing cluster alerts\"}" \
        | grep -o '"silenceID":"[^"]*"' | cut -d'"' -f4)" || die "failed to silence alert ${name}"
      info "  - ${name} (silence ${id})"
    done <<< "${names}"
  fi

  kill "${PF_PID}" 2>/dev/null || true
  wait "${PF_PID}" 2>/dev/null || true
  PF_PID=""
}

# ---------------------------------------------------------------------------
# 9. Alerts adapter opt-in
# ---------------------------------------------------------------------------
enable_adapter() {
  log "Enabling the alerts adapter (allowedReceivers: [Critical]) and restarting it"
  write_adapter_config "
  - Critical"
  restart_adapter
  info "adapter restarted"
}

# ---------------------------------------------------------------------------
main() {
  preflight
  install_acm_operator
  configure_mch
  import_hub_self
  deploy_agentic_operator
  deploy_hub
  configure_hubconfig
  configure_llm_and_approvals
  silence_critical_alerts
  enable_adapter

  log "Setup complete. Run the alert pipeline e2e test with:"
  cat <<EOF
    export MC_HUB_KUBECONFIG=${KUBECONFIG}
    export MC_SPOKE_KUBECONFIG=${KUBECONFIG}
    export MC_SPOKE_NAME=${SPOKE_NAME}
    make alert-pipeline-e2e          # add SKIP_LLM_STAGES=true for Phase 1 only
EOF
}

main "$@"
