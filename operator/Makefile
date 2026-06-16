# dotvirt installer operator — Kubebuilder-style targets. Tools are pinned and run
# via `go run` (no global installs). Build from this directory; the operator module
# resolves the app module via the replace in go.mod (and the root go.work).
IMG ?= registry.desku.be/dotvirt-operator:latest
VERSION ?= 0.0.0
CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0
KUSTOMIZE ?= go run sigs.k8s.io/kustomize/kustomize/v5@v5.4.3
OPERATOR_SDK ?= operator-sdk

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
	cd .. && docker build -f operator/Dockerfile -t $(IMG) .

.PHONY: deploy
deploy: manifests ## Apply the CRD + operator to the current cluster (needs config/default).
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: bundle
bundle: manifests ## Generate the OLM bundle for OperatorHub (needs operator-sdk on PATH).
	$(OPERATOR_SDK) generate kustomize manifests -q
	$(KUSTOMIZE) build config/manifests | $(OPERATOR_SDK) generate bundle --overwrite --version $(VERSION)
	$(OPERATOR_SDK) bundle validate ./bundle
