# Build the manager binary
FROM registry.redhat.io/ubi9/go-toolset:9.8-1786495588 AS builder
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

RUN CGO_ENABLED=1 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -tags strictfipsruntime -o manager ./cmd/

FROM registry.redhat.io/ubi9/ubi-minimal:9.8-1786380870

WORKDIR /
COPY --from=builder /workspace/manager .
RUN mkdir /licenses
COPY LICENSE /licenses/.
LABEL name="openshift-lightspeed/lightspeed-hub-rhel9-operator" \
      cpe="cpe:/a:redhat:openshift_lightspeed:1::el9" \
      com.redhat.component="openshift-lightspeed" \
      io.k8s.display-name="OpenShift Lightspeed Hub Operator" \
      summary="OpenShift Lightspeed Hub Operator manages multicluster spoke lifecycle and fleet coordination." \
      description="OpenShift Lightspeed Hub Operator runs on a central hub cluster and manages a fleet of spoke clusters for OpenShift Lightspeed multicluster operations." \
      io.k8s.description="OpenShift Lightspeed Hub Operator is a component of OpenShift Lightspeed for multicluster spoke management." \
      io.openshift.tags="openshift-lightspeed,hub,multicluster,ols"
USER 65532:65532

ENTRYPOINT ["/manager"]
