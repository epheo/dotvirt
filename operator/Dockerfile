# Build from the REPO ROOT so both modules are in context (the operator imports the
# app module via the replace directive):
#   docker build -f operator/Dockerfile -t quay.io/epheo/dotvirt-operator:<tag> .
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
# GOWORK=off so the operator builds standalone via its replace directive: the
# repo-root go.work is copied in by `COPY . .`, and workspace mode rejects -mod=mod.
RUN cd operator && CGO_ENABLED=0 GOWORK=off GOFLAGS=-mod=mod go build -trimpath -o /out/manager ./cmd

FROM gcr.io/distroless/static:nonroot
ARG VERSION=dev
# OCI + Red Hat/OpenShift image metadata. The name/vendor/version/release/summary/
# description/maintainer labels plus the /licenses dir are what the certified-operator
# preflight checks expect; the org.opencontainers.* + io.* labels are good practice.
LABEL name="dotvirt-operator" \
      vendor="epheo" \
      version="${VERSION}" \
      release="1" \
      summary="OLM operator that installs dotvirt — a vCenter-like GitOps WebUI for KubeVirt — and its platform/tenant tier." \
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
