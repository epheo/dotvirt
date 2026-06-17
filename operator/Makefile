# dotvirt installer operator — Kubebuilder-style targets. Tools are pinned and run
# via `go run` (no global installs). Build from this directory; the operator module
# resolves the app module via the replace in go.mod (and the root go.work).
VERSION ?= 0.0.0
# Images live under quay.io/epheo. IMG defaults to the version tag (never :latest);
# a release overrides it with the immutable @sha256 digest.
IMG ?= quay.io/epheo/dotvirt-operator:v$(VERSION)
BUNDLE_IMG ?= quay.io/epheo/dotvirt-operator-bundle:v$(VERSION)
CATALOG_IMG ?= quay.io/epheo/dotvirt-operator-catalog:v$(VERSION)
CONTAINER_TOOL ?= docker
CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0
KUSTOMIZE ?= go run sigs.k8s.io/kustomize/kustomize/v5@v5.4.3
OPERATOR_SDK ?= operator-sdk
OPM ?= go run github.com/operator-framework/operator-registry/cmd/opm@v1.51.0

.PHONY: help
help: ## Show targets.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-14s %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Generate DeepCopy methods (zz_generated.deepcopy.go).
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: ## Generate the CRD + RBAC role into config/.
	$(CONTROLLER_GEN) crd rbac:roleName=dotvirt-operator paths="./..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac

.PHONY: fmt vet
fmt: ; go fmt ./...
vet: ; go vet ./...

.PHONY: build
build: generate fmt vet ## Build the manager binary.
	go build -o bin/manager ./cmd

.PHONY: install
install: manifests ## Install the CRD into the current cluster.
	kubectl apply -f config/crd/bases

.PHONY: uninstall
uninstall: ## Remove the CRD (and any Dotvirt CRs) from the current cluster.
	kubectl delete --ignore-not-found -f config/crd/bases

.PHONY: run
run: generate ## Run the controller against the current kubecontext. Pass ARGS=-dry-run to validate without persisting.
	go run ./cmd $(ARGS)

.PHONY: docker-build
docker-build: ## Build the operator image (run from the REPO ROOT context).
	cd .. && $(CONTAINER_TOOL) build -f operator/Dockerfile -t $(IMG) .

.PHONY: deploy
deploy: manifests ## Apply the CRD + operator to the current cluster (needs config/default).
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: bundle
bundle: manifests ## Generate the OLM bundle for OperatorHub (needs operator-sdk on PATH).
	$(OPERATOR_SDK) generate kustomize manifests -q
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle --overwrite --version $(VERSION)
	# operator-sdk regenerates annotations.yaml + bundle.Dockerfile, so re-inject the
	# OpenShift version floor (which OCP releases embedded OperatorHub offers this on).
	grep -q com.redhat.openshift.versions bundle/metadata/annotations.yaml || \
		echo '  com.redhat.openshift.versions: "v4.18"' >> bundle/metadata/annotations.yaml
	grep -q com.redhat.openshift.versions bundle.Dockerfile || \
		echo 'LABEL com.redhat.openshift.versions="v4.18"' >> bundle.Dockerfile
	# operator-sdk doesn't bundle scorecard automatically — add the config + the
	# test annotations/labels + the image COPY so the published bundle is scorecard-able.
	mkdir -p bundle/tests/scorecard
	$(KUSTOMIZE) build config/scorecard > bundle/tests/scorecard/config.yaml
	grep -q test.config.v1 bundle/metadata/annotations.yaml || printf '  operators.operatorframework.io.test.config.v1: tests/scorecard/\n  operators.operatorframework.io.test.mediatype.v1: scorecard+v1\n' >> bundle/metadata/annotations.yaml
	grep -q test.config.v1 bundle.Dockerfile || printf 'COPY bundle/tests/scorecard /tests/scorecard/\nLABEL operators.operatorframework.io.test.config.v1=tests/scorecard/\nLABEL operators.operatorframework.io.test.mediatype.v1=scorecard+v1\n' >> bundle.Dockerfile
	$(OPERATOR_SDK) bundle validate ./bundle

.PHONY: scorecard
scorecard: ## Run scorecard (basic + olm suites) against ./bundle (needs a cluster).
	$(OPERATOR_SDK) scorecard ./bundle --wait-time 120s

.PHONY: bundle-build bundle-push catalog catalog-build catalog-push
bundle-build: ## Build the bundle image ($(BUNDLE_IMG)).
	$(CONTAINER_TOOL) build -f bundle.Dockerfile -t $(BUNDLE_IMG) .
bundle-push: ## Push the bundle image.
	$(CONTAINER_TOOL) push $(BUNDLE_IMG)

catalog: ## Render the File-Based Catalog from catalog-template.yaml (needs BUNDLE_IMG pushed).
	mkdir -p catalog/dotvirt-operator
	$(OPM) alpha render-template basic catalog-template.yaml -o yaml > catalog/dotvirt-operator/catalog.yaml
	$(OPM) validate catalog
catalog-build: ## Build the catalog image ($(CATALOG_IMG)).
	$(CONTAINER_TOOL) build -f catalog.Dockerfile -t $(CATALOG_IMG) .
catalog-push: ## Push the catalog image.
	$(CONTAINER_TOOL) push $(CATALOG_IMG)
