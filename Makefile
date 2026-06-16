# dotvirt build/test entry points. The same targets back .github/workflows/ci.yaml.
#
# Image: quay.io/epheo/dotvirt, tagged with the immutable short-commit SHA — never a
# moving :latest (references pin the @sha256 digest). Push needs `podman login quay.io`.

REGISTRY ?= quay.io
IMAGE    ?= $(REGISTRY)/epheo/dotvirt
TAG      ?= $(shell git rev-parse --short HEAD)

.PHONY: build test web check e2e image push run

build:
	go build -o dotvirt ./cmd/dotvirt

test:
	go vet ./...
	go test ./...

web:
	cd web && npm ci && npm run build

check:
	cd web && npm run check

# Playwright e2e needs the dev stack up against a live cluster (see web/e2e).
e2e:
	cd web && npx playwright test

image:
	podman build -f Containerfile -t $(IMAGE):$(TAG) .

push: image
	podman push $(IMAGE):$(TAG)

run: build
	./dotvirt
