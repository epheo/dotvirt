# Build from the REPO ROOT: the operator's go.mod resolves ../pkg/forge and
# ../pkg/tenancy from the tree.
#   docker build -f operator/Dockerfile -t quay.io/epheo/dotvirt-operator:<tag> .
# CGO is disabled, so the manager cross-compiles for $TARGETARCH on the native build host
# (no QEMU); buildx sets TARGETOS/TARGETARCH per target platform.
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build
ENV GOTOOLCHAIN=auto CGO_ENABLED=0 GOFLAGS="-buildvcs=false -mod=readonly" \
    GOPROXY="https://proxy.golang.org|direct"
WORKDIR /src/operator
COPY operator/go.mod operator/go.sum ./
COPY pkg/forge/go.mod /src/pkg/forge/
COPY pkg/tenancy/go.mod /src/pkg/tenancy/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# Declared after the download so every arch shares that layer.
ARG TARGETOS TARGETARCH
COPY pkg/forge /src/pkg/forge
COPY pkg/tenancy /src/pkg/tenancy
COPY operator/api api
COPY operator/cmd cmd
COPY operator/internal internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/manager ./cmd

FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
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
