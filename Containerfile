# Multi-stage build: the SvelteKit SPA is built static and packed alongside the Go
# binary, which serves both /api and the SPA at the same origin (-static-dir=/web).

# --- Stage 1: build the SPA (adapter-static → /web/build) ---
# Pinned to the build host's arch (the SPA output is arch-neutral static assets, so there's
# no point emulating it per target platform).
FROM --platform=$BUILDPLATFORM docker.io/library/node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Stage 2: build the static Go binary ---
# CGO is disabled, so the Go toolchain cross-compiles for $TARGETARCH on the native build
# host (no QEMU) — buildx sets TARGETOS/TARGETARCH per target platform.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26 AS build
ENV GOTOOLCHAIN=auto CGO_ENABLED=0 GOFLAGS=-buildvcs=false
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /dotvirt ./cmd/dotvirt

# --- Stage 3: minimal runtime ---
FROM gcr.io/distroless/static:nonroot
ARG VERSION=dev
# OCI + Red Hat/OpenShift image metadata (mirrors the operator image; the
# name/vendor/version/release/summary/description/maintainer labels + /licenses dir
# are what the certified-operator preflight expects).
LABEL name="dotvirt" \
      vendor="epheo" \
      version="${VERSION}" \
      release="1" \
      summary="dotvirt — a vCenter-like GitOps WebUI for KubeVirt." \
      description="dotvirt serves a vCenter-like web console for KubeVirt that reads git/cluster/Argo and proposes pull requests, riding the user's RBAC. Single binary serving the SPA + API." \
      maintainer="epheo <github@epheo.eu>" \
      org.opencontainers.image.title="dotvirt" \
      org.opencontainers.image.description="A vCenter-like GitOps WebUI for KubeVirt." \
      org.opencontainers.image.source="https://github.com/epheo/dotvirt" \
      org.opencontainers.image.url="https://github.com/epheo/dotvirt" \
      org.opencontainers.image.vendor="epheo" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.k8s.display-name="dotvirt" \
      io.k8s.description="A vCenter-like GitOps WebUI for KubeVirt." \
      io.openshift.tags="dotvirt,kubevirt,gitops,argocd,virtualization"
COPY --from=build /dotvirt /dotvirt
COPY --from=web /web/build /web
COPY LICENSE.md /licenses/LICENSE.md
ENV DOTVIRT_STATIC_DIR=/web
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/dotvirt"]
