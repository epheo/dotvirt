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

// templateHeader says how to fill and apply the template; the OperatorGuard
// condition names this install's values.
const templateHeader = `# The dotvirt operator guard: admission policies bounding the operator's own
# account. A cluster admin installs it from the console (Install). Without the
# console, fill the placeholders and apply it; the operator's OperatorGuard
# condition names each value for its install, and an unset image stays "".
#
#   export OPERATOR_USERNAME=system:serviceaccount:<operator namespace>:` + OperatorDeployment + `
#   export INSTALL_NAMESPACE=<the Dotvirt resource's namespace>
#   export ARGO_NAMESPACE=<the ArgoCD namespace> ARGO_CONTROLLER=<its application controller ServiceAccount>
#   export SPEC_IMAGE="$(kubectl -n "$INSTALL_NAMESPACE" get dotvirt <name> -o jsonpath='{.spec.image}')"
#   export RELATED_IMAGE_DOTVIRT="$(kubectl -n <operator namespace> get deploy ` + OperatorDeployment + ` \
#     -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="RELATED_IMAGE_DOTVIRT")].value}')"
#   export RELATED_IMAGE_FORGEJO="$(kubectl -n <operator namespace> get deploy ` + OperatorDeployment + ` \
#     -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="RELATED_IMAGE_FORGEJO")].value}')"
#   envsubst < guard.yaml | ` + ApplyCommand + ` -
#
# Without envsubst, sed with the named placeholders does the same:
#   sed -e "s|\${OPERATOR_USERNAME}|$OPERATOR_USERNAME|g" -e "s|\${INSTALL_NAMESPACE}|$INSTALL_NAMESPACE|g" \
#     -e "s|\${ARGO_NAMESPACE}|$ARGO_NAMESPACE|g" -e "s|\${ARGO_CONTROLLER}|$ARGO_CONTROLLER|g" \
#     -e "s|\${SPEC_IMAGE}|$SPEC_IMAGE|g" -e "s|\${RELATED_IMAGE_DOTVIRT}|$RELATED_IMAGE_DOTVIRT|g" \
#     -e "s|\${RELATED_IMAGE_FORGEJO}|$RELATED_IMAGE_FORGEJO|g" guard.yaml | ` + ApplyCommand + ` -
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
	for _, o := range Guard(TemplateParams) {
		y, err := yaml.Marshal(o.Object.Object)
		if err != nil {
			return nil, err
		}
		b.WriteString("---\n# " + o.Purpose + "\n")
		b.Write(y)
	}
	return b.Bytes(), nil
}
