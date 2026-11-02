# Multi-stage build: the SvelteKit SPA is built static and packed alongside the Go
# binary, which serves both /api and the SPA at the same origin (-static-dir=/web).

# --- Stage 1: build the SPA (adapter-static → /web/build) ---
# Pinned to the build host's arch (the SPA output is arch-neutral static assets, so there's
# no point emulating it per target platform).
FROM --platform=$BUILDPLATFORM docker.io/library/node:26-alpine@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Stage 2: build the static Go binary ---
# CGO is disabled, so the Go toolchain cross-compiles for $TARGETARCH on the native build
# host (no QEMU) — buildx sets TARGETOS/TARGETARCH per target platform.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
# GOWORK=off: the repo-root go.work arrives with `COPY . .` and would change the build
# list after `go mod download`, sending the build to the network uncached.
ENV GOTOOLCHAIN=auto CGO_ENABLED=0 GOWORK=off GOFLAGS="-buildvcs=false -mod=readonly" \
    GOPROXY="https://proxy.golang.org|direct"
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /dotvirt ./cmd/dotvirt

# --- Stage 3: minimal runtime ---
FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG VERSION=dev
# OCI + Red Hat/OpenShift image metadata (mirrors the operator image; the
# name/vendor/version/release/summary/description/maintainer labels + /licenses dir
# are what the certified-operator preflight expects).
LABEL name="dotvirt" \
      vendor="epheo" \
      version="${VERSION}" \
      release="1" \
      summary="dotvirt - a point-and-click GitOps WebUI for KubeVirt." \
      description="dotvirt serves a point-and-click web console for KubeVirt that reads git/cluster/Argo and proposes pull requests, riding the user's RBAC. Single binary serving the SPA + API." \
      maintainer="epheo <github@epheo.eu>" \
      org.opencontainers.image.title="dotvirt" \
      org.opencontainers.image.description="A point-and-click GitOps WebUI for KubeVirt." \
      org.opencontainers.image.source="https://github.com/epheo/dotvirt" \
      org.opencontainers.image.url="https://github.com/epheo/dotvirt" \
      org.opencontainers.image.vendor="epheo" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.k8s.display-name="dotvirt" \
      io.k8s.description="A point-and-click GitOps WebUI for KubeVirt." \
      io.openshift.tags="dotvirt,kubevirt,gitops,argocd,virtualization"
COPY --from=build /dotvirt /dotvirt
COPY --from=web /web/build /web
COPY LICENSE.md /licenses/LICENSE.md
ENV DOTVIRT_STATIC_DIR=/web
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/dotvirt"]
