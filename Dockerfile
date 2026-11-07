# Build the manager binary
FROM registry.redhat.io/ubi9/go-toolset:9.8-1793736700 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

# this directory is checked by ecosystem-cert-preflight-checks task in Konflux
COPY LICENSE /licenses/

USER 0

# Build with Go's native FIPS 140-3 crypto provider (pure Go, no CGo/OpenSSL).
# GOFIPS140=certified selects the certified FIPS module; the binary defaults
# to fips140=on (see the //go:debug directive in cmd/fips.go), keeping
# FIPS-approved crypto always active (matching the previous
# strictfipsruntime behavior).
# GOAMD64 is only consulted when GOARCH=amd64; it is silently ignored for
# other target architectures (arm64 etc.), so multi-arch builds are unaffected.
RUN CGO_ENABLED=0 GOFIPS140=certified GOAMD64=v2 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager ./cmd/
# Verify manager targets at most x86-64-v2 (no-op on non-amd64)
RUN go build -o check-isa-level ./cmd/check-isa-level && ./check-isa-level ./manager

FROM registry.access.redhat.com/ubi9-micro:9.8-1791765224@sha256:ff30602252a3e92706585c7c34cd3c74028eaac4ab815e6b19f8b27562bdd7e3

WORKDIR /
COPY --from=builder /workspace/manager .
# Public CA bundle: spoke kubeconfigs may omit certificate-authority-data, in
# which case client-go verifies the spoke API server against system trust
# roots. Pure PEM data, parsed natively by Go - no OpenSSL in the runtime image.
# (In the ubi9-based builder, /etc/pki/tls/certs/ca-bundle.crt is a symlink to
# this file, so the real file is copied to the path Go's x509 checks.)
COPY --from=builder /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/pki/tls/certs/ca-bundle.crt
RUN mkdir /licenses
COPY LICENSE /licenses/.
LABEL name="openshift-lightspeed/lightspeed-hub-rhel9-operator" \
      cpe="cpe:/a:redhat:openshift_lightspeed:2::el9" \
      com.redhat.component="openshift-lightspeed" \
      io.k8s.display-name="OpenShift Lightspeed Hub Operator" \
      summary="OpenShift Lightspeed Hub Operator manages multicluster spoke lifecycle and fleet coordination." \
      description="OpenShift Lightspeed Hub Operator runs on a central hub cluster and manages a fleet of spoke clusters for OpenShift Lightspeed multicluster operations." \
      io.k8s.description="OpenShift Lightspeed Hub Operator is a component of OpenShift Lightspeed for multicluster spoke management." \
      io.openshift.tags="openshift-lightspeed,hub,multicluster,ols"
USER 65532:65532

ENTRYPOINT ["/manager"]
