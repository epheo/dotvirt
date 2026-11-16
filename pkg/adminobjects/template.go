package adminobjects

import (
	"bytes"

	"sigs.k8s.io/yaml"
)

// TemplateParams are the release template's placeholders, filled by envsubst.
var TemplateParams = Params{
	Operator:       "${OPERATOR_USERNAME}",
	Namespace:      "${INSTALL_NAMESPACE}",
	ArgoNamespace:  "${ARGO_NAMESPACE}",
	ArgoController: "${ARGO_CONTROLLER}",
	SpecImage:      "${SPEC_IMAGE}",
	DotvirtImage:   "${RELATED_IMAGE_DOTVIRT}",
	ForgejoImage:   "${RELATED_IMAGE_FORGEJO}",
}

// templateHeader says how to fill and apply the template, each value derived
// from the cluster: never from the operator's status, which it writes.
const templateHeader = `# The dotvirt operator guard: admission policies bounding the operator's own
# account. A cluster admin installs it from the console (Install). Without the
# console, derive each value from the cluster yourself (never copy one from the
# operator's status or conditions, which the operator writes), fill the
# placeholders and apply it.
#
#   # The install's namespace is the console's: tenants may create Dotvirts in
#   # their own namespaces, so take the one ClusterRoleBinding dotvirt grants the
#   # console's ServiceAccount in, and the one Dotvirt there, as the console does.
#   # After moving the install, the binding names the old namespace until the
#   # operator reconciles it: use the new namespace then.
#   export INSTALL_NAMESPACE="$(kubectl get clusterrolebinding dotvirt \
#     -o jsonpath='{.subjects[?(@.kind=="ServiceAccount")].namespace}')"
#   # The operator's Deployment: among those named ` + OperatorDeployment + ` (a
#   # tenant may name one so), the one whose account may patch Dotvirt status there:
#   OP="$(kubectl get deploy -A --field-selector metadata.name=` + OperatorDeployment + ` \
#     -o jsonpath='{range .items[*]}{.metadata.namespace}/{.spec.template.spec.serviceAccountName}{"\n"}{end}' |
#     while IFS=/ read -r ns sa; do kubectl auth can-i patch dotvirts.dotvirt.io --subresource=status \
#       -n "$INSTALL_NAMESPACE" --as="system:serviceaccount:$ns:${sa:-default}" >/dev/null && echo "$ns/${sa:-default}"; done)"
#   [ "$(printf '%s\n' "$OP" | grep -c /)" = 1 ] || echo "want exactly one operator, found: $OP"
#   export OPERATOR_USERNAME="system:serviceaccount:${OP%/*}:${OP#*/}"
#   relimg() { kubectl -n "${OP%/*}" get deploy ` + OperatorDeployment + ` \
#     -o jsonpath="{.spec.template.spec.containers[0].env[?(@.name==\"$1\")].value}"; }
#   export RELATED_IMAGE_DOTVIRT="$(relimg RELATED_IMAGE_DOTVIRT)" RELATED_IMAGE_FORGEJO="$(relimg RELATED_IMAGE_FORGEJO)"
#   spec() { kubectl -n "$INSTALL_NAMESPACE" get dotvirt -o jsonpath="{.items[0].spec.$1}"; }
#   export SPEC_IMAGE="$(spec image)"
#   export FORGE_INSECURE_TLS="$( [ "$(spec forge.insecureTLS)" = true ] && echo true || echo false )"
#   # Where users' metrics queries go: spec.metrics.url; its tenancyURL, else for
#   # https://thanos-querier.openshift-monitoring.svc:9091 the same URL on 9092;
#   # the CA, ` + MetricsCAPath + ` when spec.metrics.ca is set, else
#   # ` + ServiceCAPath + ` for an https .svc url. Empty when unset.
#   export METRICS_URL="$(spec metrics.url)" METRICS_TENANCY_URL=... METRICS_CA=...
#   # spec.argocd, else the defaults: openshift-gitops where routes are served
#   # (OpenShift, MicroShift), argocd elsewhere, and that instance's controller:
#   ARGO_NAMESPACE="$(spec argocd.namespace)"
#   [ -n "$ARGO_NAMESPACE" ] || ARGO_NAMESPACE="$(kubectl api-resources --api-group=route.openshift.io -o name \
#     | grep -q . && echo openshift-gitops || echo argocd)"
#   ARGO_CONTROLLER="$(spec argocd.controllerServiceAccount)"
#   [ -n "$ARGO_CONTROLLER" ] || ARGO_CONTROLLER="$( [ "$ARGO_NAMESPACE" = openshift-gitops ] \
#     && echo openshift-gitops-argocd-application-controller || echo argocd-application-controller)"
#   export ARGO_NAMESPACE ARGO_CONTROLLER
#
#   envsubst < guard.yaml | ` + ApplyCommand + ` -
#
# Without envsubst, sed with the named placeholders does the same:
#   sed -e "s|\${OPERATOR_USERNAME}|$OPERATOR_USERNAME|g" -e "s|\${INSTALL_NAMESPACE}|$INSTALL_NAMESPACE|g" \
#     -e "s|\${ARGO_NAMESPACE}|$ARGO_NAMESPACE|g" -e "s|\${ARGO_CONTROLLER}|$ARGO_CONTROLLER|g" \
#     -e "s|\${SPEC_IMAGE}|$SPEC_IMAGE|g" -e "s|\${FORGE_INSECURE_TLS}|$FORGE_INSECURE_TLS|g" \
#     -e "s|\${RELATED_IMAGE_DOTVIRT}|$RELATED_IMAGE_DOTVIRT|g" -e "s|\${RELATED_IMAGE_FORGEJO}|$RELATED_IMAGE_FORGEJO|g" \
#     -e "s|\${METRICS_URL}|$METRICS_URL|g" -e "s|\${METRICS_TENANCY_URL}|$METRICS_TENANCY_URL|g" \
#     -e "s|\${METRICS_CA}|$METRICS_CA|g" guard.yaml | ` + ApplyCommand + ` -
#
# Every object carries the label app.kubernetes.io/part-of=dotvirt-operator-guard.
#
# Generated from pkg/adminobjects (go test ./pkg/adminobjects -run TestReleaseTemplate -update).
`

// Template is the guard with TemplateParams' placeholders, published with
// each release for an admin without the console.
func Template() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(templateHeader)
	for _, o := range guard(TemplateParams, "${FORGE_INSECURE_TLS}", Metrics{URL: "${METRICS_URL}", TenancyURL: "${METRICS_TENANCY_URL}", CA: "${METRICS_CA}"}) {
		y, err := yaml.Marshal(o.Object.Object)
		if err != nil {
			return nil, err
		}
		b.WriteString("---\n# " + o.Purpose + "\n")
		b.Write(y)
	}
	return b.Bytes(), nil
}
