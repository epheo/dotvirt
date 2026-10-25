# Build from the REPO ROOT so both modules are in context (the operator imports the
# app module via the replace directive):
#   docker build -f operator/Dockerfile -t quay.io/epheo/dotvirt-operator:<tag> .
# CGO is disabled, so the manager cross-compiles for $TARGETARCH on the native build host
# (no QEMU) — buildx sets TARGETOS/TARGETARCH per target platform.
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc AS build
ARG TARGETOS TARGETARCH
ENV GOPROXY="https://proxy.golang.org|direct"
WORKDIR /src
COPY . .
# GOWORK=off so the operator builds standalone via its replace directive: the
# repo-root go.work is copied in by `COPY . .`, and workspace mode rejects -mod=mod.
RUN cd operator && CGO_ENABLED=0 GOWORK=off GOFLAGS=-mod=mod GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/manager ./cmd

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
ARG VERSION=dev
# OCI + Red Hat/OpenShift image metadata. The name/vendor/version/release/summary/
# description/maintainer labels plus the /licenses dir are what the certified-operator
# preflight checks expect; the org.opencontainers.* + io.* labels are good practice.
LABEL name="dotvirt-operator" \
      vendor="epheo" \
      version="${VERSION}" \
      release="1" \
      summary="OLM operator that installs dotvirt - a point-and-click GitOps WebUI for KubeVirt - and its platform/tenant tier." \
      description="Provisions a complete dotvirt install from a single Dotvirt resource: the workload + Route/Ingress, RBAC, the ArgoCD AppProject tier + ApplicationSet, the platform git repo (optionally a self-hosted Forgejo), and an org-level instant-sync webhook." \
      maintainer="epheo <github@epheo.eu>" \
      org.opencontainers.image.title="dotvirt-operator" \
      org.opencontainers.image.description="OLM operator that installs dotvirt and its platform/tenant tier." \
      org.opencontainers.image.source="https://github.com/epheo/dotvirt" \
      org.opencontainers.image.url="https://github.com/epheo/dotvirt" \
      org.opencontainers.image.vendor="epheo" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.k8s.display-name="dotvirt operator" \
      io.k8s.description="OLM operator that installs dotvirt and its platform/tenant tier." \
      io.openshift.tags="dotvirt,kubevirt,gitops,argocd,virtualization"
WORKDIR /
COPY --from=build /out/manager /manager
COPY LICENSE.md /licenses/LICENSE.md
USER 65532:65532
ENTRYPOINT ["/manager"]
