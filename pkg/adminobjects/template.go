package adminobjects

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

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
	ConsoleHost:    "${CONSOLE_HOST}",

	BrandingConfigMap:  "${BRANDING_CONFIGMAP}",
	MetricsCAConfigMap: "${METRICS_CA_CONFIGMAP}",
	ForgeSecret:        "${FORGE_SECRET}",
	OIDCSecret:         "${OIDC_SECRET}",
	OIDCSecretKey:      "${OIDC_SECRET_KEY}",
	OIDCIssuer:         "${OIDC_ISSUER}",

	ForgeURL:             "${FORGE_URL}",
	ForgeClientID:        "${FORGE_CLIENT_ID}",
	ForgeAuthorizeURL:    "${FORGE_AUTHORIZE_URL}",
	ForgeTokenURL:        "${FORGE_TOKEN_URL}",
	ForgeClientSecret:    "${FORGE_CLIENT_SECRET}",
	ForgeClientSecretKey: "${FORGE_CLIENT_SECRET_KEY}",
}

// templateHeader says how to fill and apply the template, each value derived
// from the cluster: never from the operator's status, which it writes.
var templateHeader = `# The dotvirt operator guard: admission policies bounding the operator's own
# account. A cluster admin installs it from the console (Install). Without the
# console, derive each value from the cluster yourself (never copy one from the
# operator's status or conditions, which the operator writes), fill the
# placeholders and apply it. Run the lines below as a script: it stops when it
# cannot tell which operator to pin.
#
#   # The operator's Deployment: among those named ` + OperatorDeployment + ` (a
#   # tenant may name one so), the one whose account may patch Dotvirt status in
#   # every namespace:
#   OP="$(kubectl get deploy -A --field-selector metadata.name=` + OperatorDeployment + ` \
#     -o jsonpath='{range .items[*]}{.metadata.namespace}/{.spec.template.spec.serviceAccountName}{"\n"}{end}' |
#     while IFS=/ read -r ns sa; do kubectl auth can-i patch dotvirts.dotvirt.io --subresource=status \
#       --all-namespaces --as="system:serviceaccount:$ns:${sa:-default}" >/dev/null && echo "$ns/${sa:-default}"; done)"
#   [ "$(printf '%s\n' "$OP" | grep -c /)" = 1 ] || { echo "want exactly one operator, found: $OP" >&2; exit 1; }
#   export OPERATOR_USERNAME="system:serviceaccount:${OP%/*}:${OP#*/}"
#   openv() { kubectl -n "${OP%/*}" get deploy ` + OperatorDeployment + ` \
#     -o jsonpath="{.spec.template.spec.containers[0].env[?(@.name==\"$1\")].value}"; }
#   export RELATED_IMAGE_DOTVIRT="$(openv RELATED_IMAGE_DOTVIRT)" RELATED_IMAGE_FORGEJO="$(openv RELATED_IMAGE_FORGEJO)"
#   # The install's namespace, as the operator resolves it: the one an admin names
#   # on its Deployment, else its own while a Dotvirt is there, else the one
#   # ClusterRoleBinding dotvirt grants the console's ServiceAccount in. Never a
#   # Dotvirt's own: tenants may create them in their namespaces.
#   INSTALL_NAMESPACE="$(openv ` + InstallNamespaceEnv + `)"
#   if [ -z "$INSTALL_NAMESPACE" ] && [ -n "$(kubectl -n "${OP%/*}" get dotvirt -o name)" ]; then
#     INSTALL_NAMESPACE="${OP%/*}"; fi
#   [ -n "$INSTALL_NAMESPACE" ] || INSTALL_NAMESPACE="$(kubectl get clusterrolebinding dotvirt \
#     -o jsonpath='{.subjects[?(@.kind=="ServiceAccount")].namespace}')"
#   case "$INSTALL_NAMESPACE" in ""|*" "*) echo "want one install namespace, found: '$INSTALL_NAMESPACE'" >&2; exit 1;; esac
#   export INSTALL_NAMESPACE
#   spec() { kubectl -n "$INSTALL_NAMESPACE" get dotvirt -o jsonpath="{.items[0].spec.$1}"; }
#   export SPEC_IMAGE="$(spec image)"
#   # An image in an official repository is left out: the repository admits it
#   # already, and a pinned digest would read Outdated at every release.
#   inrepo() { case "$1" in "$2":*|"$2"@sha256:*) return 0;; esac; return 1; }
#   inrepo "$SPEC_IMAGE" ` + Repositories[0] + ` && SPEC_IMAGE=
#   inrepo "$RELATED_IMAGE_DOTVIRT" ` + Repositories[0] + ` && RELATED_IMAGE_DOTVIRT=
#   inrepo "$RELATED_IMAGE_FORGEJO" ` + Repositories[1] + ` && RELATED_IMAGE_FORGEJO=
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
#   # The console's host: spec.ingress.host, else the router's host for the
#   # console's Route on the cluster's ingress domain (OpenShift), else the one
#   # host routers report serving that Route under (MicroShift), else empty:
#   CONSOLE_HOST="$(spec ingress.host)"
#   [ -n "$CONSOLE_HOST" ] || CONSOLE_HOST="$(kubectl get ingresses.config.openshift.io cluster \
#     -o jsonpath='{.spec.domain}' 2>/dev/null | sed "s|^.|dotvirt-$INSTALL_NAMESPACE.&|")"
#   [ -n "$CONSOLE_HOST" ] || CONSOLE_HOST="$(kubectl -n "$INSTALL_NAMESPACE" get route.route.openshift.io dotvirt \
#     -o jsonpath='{range .status.ingress[*]}{.host}{"\n"}{end}' 2>/dev/null | sort -u | awk 'NF { n++; h = $0 } END { if (n == 1) print h }')"
#   export CONSOLE_HOST
#   # The console's volumes an admin chose, and the forge credential Secret:
#   export BRANDING_CONFIGMAP="$(spec branding.configMap)" METRICS_CA_CONFIGMAP="$(spec metrics.ca.name)"
#   FORGE_SECRET="$(spec forge.credentialsSecret)"; export FORGE_SECRET="${FORGE_SECRET:-` + DefaultForgeSecret + `}"
#   export FORGE_MANAGED="$( [ "$(spec forge.managed)" = true ] && echo true || echo false )"
#   # The OIDC issuer and client secret an admin chose, if any:
#   export OIDC_SECRET="$(spec auth.oidc.clientSecret.name)" OIDC_SECRET_KEY="$(spec auth.oidc.clientSecret.key)"
#   export OIDC_ISSUER="$(spec auth.oidc.issuerURL)"
#   # Delegated pull requests: the forge application an admin registered.
#   export FORGE_DELEGATED="$( [ "$(spec forge.pullRequests)" = Delegated ] && echo true || echo false )"
#   export FORGE_URL="$(spec forge.url)" FORGE_CLIENT_ID="$(spec forge.userAuth.clientID)"
#   export FORGE_AUTHORIZE_URL="$(spec forge.userAuth.authorizeURL)" FORGE_TOKEN_URL="$(spec forge.userAuth.tokenURL)"
#   export FORGE_CLIENT_SECRET="$(spec forge.userAuth.clientSecret.name)" FORGE_CLIENT_SECRET_KEY="$(spec forge.userAuth.clientSecret.key)"
#   # Not delegating, the guard pins none of them:
#   [ "$FORGE_DELEGATED" = true ] || { FORGE_URL=; FORGE_CLIENT_ID=; FORGE_AUTHORIZE_URL=; FORGE_TOKEN_URL=; FORGE_CLIENT_SECRET=; FORGE_CLIENT_SECRET_KEY=; }
#
#   # Every value lands inside a quoted string; one that could end it is refused,
#   # never escaped:
#   for v in ` + placeholderList + `; do
#     case "$(printenv "$v")" in *'"'*|*\\*) echo "$v holds a quote or a backslash" >&2; exit 1;; esac
#   done
#
#   envsubst < guard.yaml | ` + ApplyCommand + ` -
#
# Without envsubst, sed with the named placeholders does the same:
#   sed -e "s|\${OPERATOR_USERNAME}|$OPERATOR_USERNAME|g" -e "s|\${INSTALL_NAMESPACE}|$INSTALL_NAMESPACE|g" \
#     -e "s|\${ARGO_NAMESPACE}|$ARGO_NAMESPACE|g" -e "s|\${ARGO_CONTROLLER}|$ARGO_CONTROLLER|g" \
#     -e "s|\${SPEC_IMAGE}|$SPEC_IMAGE|g" -e "s|\${FORGE_INSECURE_TLS}|$FORGE_INSECURE_TLS|g" \
#     -e "s|\${RELATED_IMAGE_DOTVIRT}|$RELATED_IMAGE_DOTVIRT|g" -e "s|\${RELATED_IMAGE_FORGEJO}|$RELATED_IMAGE_FORGEJO|g" \
#     -e "s|\${METRICS_URL}|$METRICS_URL|g" -e "s|\${METRICS_TENANCY_URL}|$METRICS_TENANCY_URL|g" \
#     -e "s|\${METRICS_CA}|$METRICS_CA|g" -e "s|\${CONSOLE_HOST}|$CONSOLE_HOST|g" \
#     -e "s|\${BRANDING_CONFIGMAP}|$BRANDING_CONFIGMAP|g" -e "s|\${METRICS_CA_CONFIGMAP}|$METRICS_CA_CONFIGMAP|g" \
#     -e "s|\${FORGE_SECRET}|$FORGE_SECRET|g" -e "s|\${FORGE_MANAGED}|$FORGE_MANAGED|g" \
#     -e "s|\${OIDC_SECRET}|$OIDC_SECRET|g" -e "s|\${OIDC_SECRET_KEY}|$OIDC_SECRET_KEY|g" \
#     -e "s|\${OIDC_ISSUER}|$OIDC_ISSUER|g" -e "s|\${FORGE_DELEGATED}|$FORGE_DELEGATED|g" \
#     -e "s|\${FORGE_URL}|$FORGE_URL|g" -e "s|\${FORGE_CLIENT_ID}|$FORGE_CLIENT_ID|g" \
#     -e "s|\${FORGE_AUTHORIZE_URL}|$FORGE_AUTHORIZE_URL|g" -e "s|\${FORGE_TOKEN_URL}|$FORGE_TOKEN_URL|g" \
#     -e "s|\${FORGE_CLIENT_SECRET}|$FORGE_CLIENT_SECRET|g" -e "s|\${FORGE_CLIENT_SECRET_KEY}|$FORGE_CLIENT_SECRET_KEY|g" guard.yaml | ` + ApplyCommand + ` -
#
# Every object carries the label app.kubernetes.io/part-of=dotvirt-operator-guard.
#
# Generated from pkg/adminobjects (go test ./pkg/adminobjects -run TestReleaseTemplate -update).
`

// Template is the guard with TemplateParams' placeholders, published with
// each release for an admin without the console.
func Template() ([]byte, error) {
	var body bytes.Buffer
	for _, o := range guard(TemplateParams, "${FORGE_INSECURE_TLS}", "${FORGE_MANAGED}", "${FORGE_DELEGATED}", Metrics{URL: "${METRICS_URL}", TenancyURL: "${METRICS_TENANCY_URL}", CA: "${METRICS_CA}"}) {
		y, err := yaml.Marshal(o.Object.Object)
		if err != nil {
			return nil, err
		}
		body.WriteString("---\n# " + o.Purpose + "\n")
		body.Write(y)
	}
	// The quote check covers every placeholder the body holds.
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(body.String(), -1) {
		if !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return append([]byte(strings.Replace(templateHeader, placeholderList, strings.Join(names, " "), 1)), body.Bytes()...), nil
}

// placeholderList marks where the header lists the placeholders.
const placeholderList = "<placeholders>"

var placeholder = regexp.MustCompile(`\$\{([A-Z_]+)\}`)
