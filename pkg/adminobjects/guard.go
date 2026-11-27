package adminobjects

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

// The operator guard: admission policies bounding what the operator's own
// account may write as Secrets, Deployments, RBAC bindings and its own install
// resource. Each policy and its binding share a name.
const (
	GuardSecretsName     = "dotvirt-operator-secrets"
	GuardAccountsName    = "dotvirt-operator-accounts"
	GuardDeploymentsName = "dotvirt-operator-deployments"
	GuardClaimsName      = "dotvirt-operator-claims"
	GuardBindingsName    = "dotvirt-operator-bindings"
	GuardInstallName     = "dotvirt-operator-install"
	GuardFrontDoorName   = "dotvirt-operator-frontdoor"
	GuardRoutesName      = "dotvirt-operator-routes"
	GuardIngressesName   = "dotvirt-operator-ingresses"
	GuardHTTPRoutesName  = "dotvirt-operator-httproutes"
)

// GuardNames are the guard's policy and binding names, in apply order: each
// policy then its binding, the ones that took kinds over from an earlier
// guard's policies first, so narrowing those never leaves a kind unguarded,
// even when an install stops partway.
var GuardNames = []string{GuardAccountsName, GuardClaimsName, GuardRoutesName, GuardIngressesName, GuardHTTPRoutesName,
	GuardSecretsName, GuardDeploymentsName, GuardBindingsName, GuardInstallName, GuardFrontDoorName}

// OperandAccounts are the ServiceAccounts the operator's Deployments run as:
// the console's and the managed forge's.
var OperandAccounts = []string{ConsoleAccount, ForgeAccount}

// Repositories are the official image repositories: any release in them
// passes, so an upgrade needs no new guard. The operator's built-in default
// images come from these (a test in the operator holds them together).
var Repositories = []string{"quay.io/epheo/dotvirt", "codeberg.org/forgejo/forgejo"}

// PlatformSyncAccount is the platform tier's sync account in the ArgoCD
// namespace (pkg/tenancy.PlatformSyncAccount; a test holds them together).
const PlatformSyncAccount = "dotvirt-platform-sync"

// Guard is the operator guard for p:
//   - Secrets only in the install and ArgoCD namespaces, never a
//     ServiceAccount token;
//   - Deployments only in the install namespace, as dotvirt's own accounts,
//     from the official repositories or an image an admin chose, off the host
//     and unprivileged;
//   - RBAC bindings only the ones the operator renders: their names, roles and
//     subjects;
//   - its own Dotvirt resource only its finalizers: the spec carries what an
//     admin chose (the images, the forge credential), which the console reads.
//
// The apiserver evaluates it in-process, only for those kinds' writes; the
// username condition runs first, so every other caller's write costs one
// string comparison.
func Guard(p Params) []Object {
	p = p.delegatedOnly()
	// The console's settings pinned to spec values hold them as the operator
	// renders them, escaped for the kubelet.
	p.OIDCIssuer = KubeletLiteral(p.OIDCIssuer)
	p.Metrics = Metrics{URL: KubeletLiteral(p.Metrics.URL), TenancyURL: KubeletLiteral(p.Metrics.TenancyURL), CA: KubeletLiteral(p.Metrics.CA)}
	p.ForgeURL, p.ForgeClientID = KubeletLiteral(p.ForgeURL), KubeletLiteral(p.ForgeClientID)
	p.ForgeAuthorizeURL, p.ForgeTokenURL = KubeletLiteral(p.ForgeAuthorizeURL), KubeletLiteral(p.ForgeTokenURL)
	return guard(p, strconv.FormatBool(p.InsecureTLS), strconv.FormatBool(p.ForgeManaged), strconv.FormatBool(p.ForgeDelegated), p.Metrics)
}

// guard renders with insecure, the CEL literal for p.InsecureTLS, which the
// release template leaves a placeholder.
func guard(p Params, insecure, managed, delegated string, metrics Metrics) []Object {
	var secretEnv []string
	for _, e := range ConsoleSecretEnv(p) {
		secretEnv = append(secretEnv, e[0]+"/"+e[1]+"/"+e[2])
	}
	volumes, mounts := consoleVolumes(p)
	operator := `request.userInfo.username == ` + quote(p.Operator)
	front := frontDoor(p)
	// One kind per policy: the apiserver type-checks every rule against every
	// kind its policy matches, and a rule naming another kind's fields leaves
	// a warning per rule and kind in the policy's status.
	policies := []*admissionregistrationv1.ValidatingAdmissionPolicy{
		policy(GuardSecretsName, rules("", "secrets"), operator, []admissionregistrationv1.Validation{
			// Kubernetes already requires the annotation on a token-type
			// Secret, so the annotation rule refuses both today; the type
			// rule holds if that requirement ever goes.
			deny(`!has(object.type) || object.type != `+quote(string(corev1.SecretTypeServiceAccountToken)),
				"the operator never writes ServiceAccount token Secrets"),
			deny(`!has(object.metadata.annotations) || !(`+quote(corev1.ServiceAccountNameKey)+` in object.metadata.annotations)`,
				"the operator never asks for a ServiceAccount token"),
			deny(`request.namespace in [`+quote(p.Namespace)+`, `+quote(p.ArgoNamespace)+`]`,
				"the operator writes Secrets only in "+p.Namespace+" and "+p.ArgoNamespace),
		}),
		policy(GuardAccountsName, rules("", "serviceaccounts"), operator, []admissionregistrationv1.Validation{
			// The console's pod leaves its token mount to its account, so the
			// account's setting decides whether client-go finds it.
			deny(`object.metadata.name != `+quote(ConsoleAccount)+` || `+
				`!has(object.automountServiceAccountToken) || object.automountServiceAccountToken`,
				"the operator keeps the console's ServiceAccount token mounted"),
		}),
		policy(GuardDeploymentsName, rules("apps", "deployments"), operator, []admissionregistrationv1.Validation{
			deny(`request.namespace == `+quote(p.Namespace),
				"the operator runs Deployments only in "+p.Namespace),
			deny(`object.metadata.name == `+quote(ConsoleDeployment)+` ? `+pod+`.serviceAccountName == `+quote(ConsoleAccount)+
				` : (object.metadata.name == `+quote(ForgeDeployment)+` && `+pod+`.serviceAccountName == `+quote(ForgeAccount)+`)`,
				"the operator runs only the console as "+ConsoleAccount+" and the forge as "+ForgeAccount),
			deny(`object.metadata.name == `+quote(ConsoleDeployment)+
				` ? `+eachContainer(imageFrom(Repositories[0], p.SpecImage, p.DotvirtImage))+
				` : `+eachContainer(imageFrom(Repositories[1], p.ForgejoImage)),
				"the operator runs the console only from "+Repositories[0]+" and the forge only from "+Repositories[1]+", or the image an admin chose"),
			// The console runs its image's own entrypoint and nothing before it.
			// Its arguments stay free, so a release may add flags; the ones that
			// send users' tokens elsewhere are pinned below.
			// No other process beside it either: an exec probe or hook could run
			// the console's binary again with any settings, and a shared process
			// namespace would let a sibling read its memory.
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || (`+pod+`.containers.all(c, !has(c.command) && `+noExec+`) && `+
				`(!has(`+pod+`.initContainers) || size(`+pod+`.initContainers) == 0) && `+
				`!(has(`+pod+`.shareProcessNamespace) && `+pod+`.shareProcessNamespace))`,
				"the operator runs the console with its image's own entrypoint, no exec hook or probe, no init container, no shared process namespace"),
			// The console's Service selects its pods by this label: a forge pod
			// carrying it would answer on the console's host.
			deny(`object.metadata.name != `+quote(ForgeDeployment)+` || !has(object.spec.template.metadata.labels) || `+
				`!(`+quote(ConsoleSelectorKey)+` in object.spec.template.metadata.labels && object.spec.template.metadata.labels[`+quote(ConsoleSelectorKey)+`] == `+quote(ConsoleSelectorValue)+`)`,
				"the operator gives the forge's pods none of the console's labels"),
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || `+pod+`.containers.all(c, `+consoleSettings(p, metrics, insecure, delegated)+`)`,
				"the operator sets none of the console's settings that send users' tokens or the console's pages elsewhere, but as an admin chose them"),
			// The console's apiserver endpoint and credentials are the in-cluster
			// ones: no kubeconfig, no service address of its own, its token and CA
			// mounted where client-go reads it. And no name resolves elsewhere than
			// the cluster's DNS says: the CAs it trusts for metrics and OAuth come
			// from ConfigMaps the operator writes, so a host alias or resolver of
			// the operator's choosing would let such a CA intercept users' tokens.
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || (`+
				`!(has(`+pod+`.automountServiceAccountToken) && !`+pod+`.automountServiceAccountToken) && `+
				`(!has(`+pod+`.hostAliases) || size(`+pod+`.hostAliases) == 0) && !has(`+pod+`.dnsConfig) && `+
				`(!has(`+pod+`.dnsPolicy) || `+pod+`.dnsPolicy == "ClusterFirst") && `+
				pod+`.containers.all(c, !has(c.envFrom) && (!has(c.env) || c.env.all(e, e.name != "KUBECONFIG" && !e.name.startsWith("KUBERNETES_SERVICE_")))))`,
				"the operator runs the console with its own ServiceAccount token, the cluster's DNS, and no kubeconfig or apiserver address of its own"),
			deny(eachContainer(`!has(c.volumeMounts) || c.volumeMounts.all(m, `+mountClear+`)`),
				"the operator mounts nothing at, above or inside the ServiceAccount token directory"),
			// A file mounted over the console's binary or its trust store runs
			// or trusts what the operator chose.
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || ((!has(`+pod+`.volumes) || `+pod+`.volumes.all(v, `+volumes+`)) && `+
				pod+`.containers.all(c, !has(c.volumeMounts) || c.volumeMounts.all(m, (!has(m.subPath) || m.subPath == "") && `+
				`(!has(m.subPathExpr) || m.subPathExpr == "") && (`+mounts+`))))`,
				"the operator gives the console only its own volumes, from the claim, ConfigMaps and Secret rendered or an admin chose, each mounted whole at its own path"),
			// The forge holds no user token, but its pod could read any Secret in
			// the install namespace and serve it.
			deny(`object.metadata.name != `+quote(ForgeDeployment)+` || (`+eachContainer(`(!has(c.envFrom) || c.envFrom.all(e, !has(e.secretRef))) && `+
				`(!has(c.env) || c.env.all(e, !has(e.valueFrom) || !has(e.valueFrom.secretKeyRef) || `+
				`(has(e.valueFrom.secretKeyRef.name) && (e.valueFrom.secretKeyRef.name == `+quote(ForgeAdminSecret)+` || `+
				`(size(`+quote(p.ForgeOIDCSecret)+`) > 0 && e.valueFrom.secretKeyRef.name == `+quote(p.ForgeOIDCSecret)+` && `+
				`has(e.valueFrom.secretKeyRef.key) && e.valueFrom.secretKeyRef.key == `+quote(p.ForgeOIDCSecretKey)+`)))))`)+`)`,
				"the operator gives the forge no Secret but its own admin password and the issuer client secret an admin chose"),
			// The volume kinds the forge renders, and no other: it runs its own
			// commands, so a network volume (nfs, iscsi, rbd, ...) would reach
			// any export the nodes can, and a Secret, projected, CSI or
			// ephemeral one data it was never given.
			deny(`object.metadata.name != `+quote(ForgeDeployment)+` || !has(`+pod+`.volumes) || `+pod+`.volumes.all(v, `+
				`(has(v.persistentVolumeClaim) && v.persistentVolumeClaim.claimName == `+quote(ForgeClaim)+`) || has(v.emptyDir) || `+
				`(has(v.configMap) && has(v.configMap.name) && (v.configMap.name == `+quote(IngressCAConfigMap)+` || `+
				`(size(`+quote(p.ForgeOIDCCAConfigMap)+`) > 0 && v.configMap.name == `+quote(p.ForgeOIDCCAConfigMap)+`))))`,
				"the operator gives the forge only its own claim, a scratch directory, the router CA and the issuer CA an admin chose"),
			// The kubelet resolves any Secret in the namespace a pod names,
			// whatever the operator may read: an env the console sends out
			// (the forge token, to the forge URL) could carry another Secret.
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || `+pod+`.containers.all(c, !has(c.env) || c.env.all(e, `+
				`!has(e.valueFrom) || !has(e.valueFrom.secretKeyRef) || (has(e.valueFrom.secretKeyRef.name) && `+
				`(e.name + "/" + e.valueFrom.secretKeyRef.name + "/" + e.valueFrom.secretKeyRef.key) in `+list(secretEnv...)+`)))`,
				"the operator gives the console only its own Secrets and the ones an admin chose, each under its rendered env name and key"),
			// A credential an admin brought goes only to the forge its Secret
			// names; a managed forge's the operator mints itself.
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || `+managed+` || `+pod+`.containers.all(c, `+
				`(!has(c.args) || c.args.all(a, !`+flagForms("forge-url")+`)) && `+
				`(!has(c.env) || c.env.all(e, e.name != "DOTVIRT_FORGE_URL" || (has(e.valueFrom) && has(e.valueFrom.secretKeyRef)))))`,
				"the operator sends a forge credential an admin brought only to the forge its Secret names"),
			deny(`!(has(`+pod+`.hostNetwork) && `+pod+`.hostNetwork) && !(has(`+pod+`.hostPID) && `+pod+`.hostPID) && !(has(`+pod+`.hostIPC) && `+pod+`.hostIPC)`,
				"the operator runs no Deployment in the host network, PID or IPC namespace"),
			deny(`!has(`+pod+`.volumes) || `+pod+`.volumes.all(v, !has(v.hostPath))`,
				"the operator mounts no host path"),
			deny(`!(has(`+pod+`.securityContext) && has(`+pod+`.securityContext.runAsUser) && `+pod+`.securityContext.runAsUser == 0)`,
				"the operator runs no pod as root"),
			deny(eachContainer(`!(has(c.securityContext) && has(c.securityContext.privileged) && c.securityContext.privileged)`),
				"the operator runs no privileged container"),
			deny(eachContainer(`has(c.securityContext) && has(c.securityContext.allowPrivilegeEscalation) && c.securityContext.allowPrivilegeEscalation == false`),
				"the operator runs every container with allowPrivilegeEscalation false"),
			deny(eachContainer(`has(c.securityContext) && has(c.securityContext.capabilities) && has(c.securityContext.capabilities.drop) && `+
				`"ALL" in c.securityContext.capabilities.drop && !(has(c.securityContext.capabilities.add) && size(c.securityContext.capabilities.add) > 0)`),
				"the operator runs every container with all capabilities dropped and none added"),
			deny(eachContainer(`!(has(c.securityContext) && has(c.securityContext.runAsUser) && c.securityContext.runAsUser == 0)`),
				"the operator runs no container as root"),
			// The container's setting wins over the pod's, as the kubelet applies them.
			deny(eachContainer(`(has(c.securityContext) && has(c.securityContext.runAsNonRoot)) ? c.securityContext.runAsNonRoot : `+
				`(has(`+pod+`.securityContext) && has(`+pod+`.securityContext.runAsNonRoot) && `+pod+`.securityContext.runAsNonRoot)`),
				"the operator runs every container as non-root"),
			deny(eachContainer(`(has(c.securityContext) && has(c.securityContext.seccompProfile)) ? c.securityContext.seccompProfile.type in ["RuntimeDefault", "Localhost"] : `+
				`(has(`+pod+`.securityContext) && has(`+pod+`.securityContext.seccompProfile) && `+pod+`.securityContext.seccompProfile.type in ["RuntimeDefault", "Localhost"])`),
				"the operator runs every container with a RuntimeDefault or Localhost seccomp profile"),
		}),
		policy(GuardClaimsName, rules("", "persistentvolumeclaims"), operator, []admissionregistrationv1.Validation{
			// A claim the operator binds to a volume, a selector or a data
			// source of its choosing hands the console or the forge data it
			// picked, or copies another claim's. The binder sets volumeName
			// once it binds; the apiserver keeps the others immutable.
			deny(`!has(object.spec.selector) && !has(object.spec.dataSource) && !has(object.spec.dataSourceRef) && `+
				`(request.operation == "CREATE" ? (!has(object.spec.volumeName) || object.spec.volumeName == "") : `+
				unchanged("object.spec.volumeName", "oldObject.spec.volumeName")+`)`,
				"the operator binds its claims to no volume, selector or data source of its choosing"),
		}),
		policy(GuardBindingsName, append(rules("rbac.authorization.k8s.io", "clusterrolebindings"), rules("rbac.authorization.k8s.io", "rolebindings")...), operator, []admissionregistrationv1.Validation{
			deny(`request.resource.resource == "clusterrolebindings" ? object.metadata.name in `+list(slices.Sorted(mapKeys(clusterBindings(p)))...)+
				` : (object.metadata.name in `+list(slices.Sorted(mapKeys(roleBindings(p)))...)+` && request.namespace == `+quote(p.ArgoNamespace)+`)`,
				"the operator writes only the bindings it renders, by name"),
			deny(`object.roleRef.apiGroup == "rbac.authorization.k8s.io" && object.roleRef.kind == "ClusterRole" && object.roleRef.name == object.metadata.name`,
				"the operator binds each binding to the ClusterRole of its own name"),
			deny(`!has(object.subjects) || object.subjects.all(s, (s.kind + "/" + (has(s.namespace) ? s.namespace : "") + "/" + s.name) in `+
				subjectMap(p)+`[object.metadata.name])`,
				"the operator binds each binding to its rendered subjects only"),
		}),
		// UPDATE only: the operator never creates its install. dotvirts/status
		// is another resource, so status writes do not match.
		policy(GuardInstallName, rule("dotvirt.io", "v1alpha1", "dotvirts", admissionregistrationv1.Update), operator, []admissionregistrationv1.Validation{
			deny(unchanged("object.spec", "oldObject.spec"), "the operator may change only its install's finalizers, never its spec"),
			deny(unchanged("object.metadata.labels", "oldObject.metadata.labels"), "the operator may change only its install's finalizers, never its labels"),
			deny(unchanged("object.metadata.annotations", "oldObject.metadata.annotations"), "the operator may change only its install's finalizers, never its annotations"),
			deny(unchanged("object.metadata.ownerReferences", "oldObject.metadata.ownerReferences"), "the operator may change only its install's finalizers, never its owners"),
		}),
		// The console's front door: the Service and the exposure the console's
		// host reaches it by, so its login codes reach the console and nothing
		// else. The forge holds no user token; its exposures may only not claim
		// the console's host.
		policy(GuardFrontDoorName, rules("", "services"), operator, front.services),
		policy(GuardRoutesName, rules("route.openshift.io", "routes"), operator, front.routes),
		policy(GuardIngressesName, rules("networking.k8s.io", "ingresses"), operator, front.ingresses),
		policy(GuardHTTPRoutesName, rules("gateway.networking.k8s.io", "httproutes"), operator, front.httproutes),
	}
	purpose := map[string]string{
		GuardSecretsName:     "Bounds the operator's Secrets: none outside " + p.Namespace + " and " + p.ArgoNamespace + ", never a ServiceAccount token",
		GuardAccountsName:    "Bounds the operator's ServiceAccounts: the console's keeps its token mounted",
		GuardDeploymentsName: "Bounds the operator's Deployments: the console and the forge only, in " + p.Namespace + ", with their own accounts, images, commands, volumes, Secrets and token, unprivileged and off the host",
		GuardClaimsName:      "Bounds the operator's claims: bound to no volume, selector or data source of its choosing",
		GuardBindingsName:    "Bounds the operator's RBAC bindings: only the ones it renders, to their own roles and rendered subjects",
		GuardInstallName:     "Bounds the operator's writes to its install: its finalizers only, never the spec an admin wrote",
		GuardFrontDoorName:   "Bounds the operator's Services: the console's selects only the console, and none leaves the cluster",
		GuardRoutesName:      "Bounds the operator's Routes: the console's leads only to the console on its host, no other claims that host",
		GuardIngressesName:   "Bounds the operator's Ingresses: the console's leads only to the console on its host, no other claims that host or every host",
		GuardHTTPRoutesName:  "Bounds the operator's HTTPRoutes: the console's leads only to the console on its host, unfiltered, no other claims that host or every host",
	}
	byName := map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{}
	for _, pol := range policies {
		byName[pol.Name] = pol
	}
	var out []Object
	for _, name := range GuardNames {
		out = append(out, Object{Purpose: purpose[name], Object: toUnstructured(byName[name])})
		out = append(out, Object{Purpose: "Enforces " + name, Object: toUnstructured(&admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicyBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: guardLabels()},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        name,
				ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
			},
		})})
	}
	return out
}

// consoleSettings is console container c setting nothing that sends users'
// tokens, or the pages they run, anywhere an admin did not choose, by flag
// (-name, --name, with =value; the bare form is never rendered) or by env:
//   - the metrics endpoints and their CA, where each user's queries go with
//     the user's token: only as rendered from the admin's spec.metrics;
//   - the OpenShift OAuth CA: absent, or the router CA the operator mounts;
//   - the cross-origin allowance: never (same-origin is rendered; an origin
//     there would be trusted with every user's session);
//   - TLS verification off: only with the admin's spec.forge.insecureTLS;
//   - a kubeconfig, or a directory of pages to serve: never (the image sets
//     its own pages; served from elsewhere they would run as every user);
//   - proxies and the system trust store: never. The operator renders none;
//     a cluster that needs a proxy would pin it to the cluster's Proxy object.
//
// The rest stays free, so a release may add settings. Among them, kept free
// for a reason: the public URL and the OIDC client settings, since the
// provider sends codes only to the redirect URI an admin registered (the
// OpenShift SSO client the console registers itself is derived from admin
// data, not from these); and the OIDC issuer, which can only phish
// (SECURITY.md keeps it open).
// flagForms is argument a being flag, in any form Go's flag package parses:
// -flag, --flag, -flag=value, --flag=value.
func flagForms(flag string) string {
	return `(a == ` + quote("-"+flag) + ` || a == ` + quote("--"+flag) +
		` || a.startsWith(` + quote("-"+flag+"=") + `) || a.startsWith(` + quote("--"+flag+"=") + `))`
}

func consoleSettings(p Params, m Metrics, insecure, delegated string) string {
	forms := flagForms
	never := func(flag, env string) string {
		return `(!has(c.args) || c.args.all(a, !` + forms(flag) + `)) && (!has(c.env) || c.env.all(e, e.name != ` + quote(env) + `))`
	}
	// pinned admits flag and env only at one of values, each matched whole.
	pinned := func(flag, env string, values ...string) string {
		var args []string
		for _, v := range values {
			args = append(args, "-"+flag+"="+v, "--"+flag+"="+v)
		}
		return `(!has(c.args) || c.args.all(a, !` + forms(flag) + ` || a in ` + list(args...) + `)) && ` +
			`(!has(c.env) || c.env.all(e, e.name != ` + quote(env) + ` || (!has(e.valueFrom) && (has(e.value) ? e.value : "") in ` + list(values...) + `)))`
	}
	return strings.Join([]string{
		pinned("metrics-url", "DOTVIRT_METRICS_URL", m.URL),
		pinned("metrics-tenancy-url", "DOTVIRT_METRICS_TENANCY_URL", m.TenancyURL),
		pinned("metrics-ca", "DOTVIRT_METRICS_CA", m.CA),
		pinned("oauth-ca", "DOTVIRT_OAUTH_CA", "", IngressCAPath),
		pinned("ui-origin", "DOTVIRT_UI_ORIGIN", ""),
		// The console sends the OIDC client secret an admin chose to the
		// issuer's token endpoint at every sign-in.
		pinned("oidc-issuer", "DOTVIRT_OIDC_ISSUER", p.OIDCIssuer),
		// Trust for that issuer: only the CA an admin chose, mounted.
		pinned("oidc-ca", "DOTVIRT_OIDC_CA", "", OIDCCAPath),
		// Delegated: each user's forge token goes to the delegated URL (the
		// API) and the token URL; the forge application's client secret to
		// the token URL, and only from the admin's Secret (ConsoleSecretEnv).
		// Not delegating, none of them is set.
		`(` + delegated + ` ? (` + strings.Join([]string{
			pinned("forge-delegated-url", "DOTVIRT_FORGE_DELEGATED_URL", p.ForgeURL),
			pinned("forge-client-id", "DOTVIRT_FORGE_CLIENT_ID", p.ForgeClientID),
			pinned("forge-authorize-url", "DOTVIRT_FORGE_AUTHORIZE_URL", p.ForgeAuthorizeURL),
			pinned("forge-token-url", "DOTVIRT_FORGE_TOKEN_URL", p.ForgeTokenURL),
		}, " && ") + `) : (` + strings.Join([]string{
			never("forge-delegated", "DOTVIRT_FORGE_DELEGATED"),
			never("forge-delegated-url", "DOTVIRT_FORGE_DELEGATED_URL"),
			never("forge-client-id", "DOTVIRT_FORGE_CLIENT_ID"),
			never("forge-authorize-url", "DOTVIRT_FORGE_AUTHORIZE_URL"),
			never("forge-token-url", "DOTVIRT_FORGE_TOKEN_URL"),
			never("forge-client-secret", "DOTVIRT_FORGE_CLIENT_SECRET"),
		}, " && ") + `))`,
		pinned("forge-ca", "DOTVIRT_FORGE_CA", "", IngressCAPath),
		// The kubelet expands $(NAME) in a container's command, args and env
		// values, Secret-backed env included: a setting the console sends out
		// (a webhook URL, a URL every browser loads) would carry a Secret's
		// value. The render escapes every $ (KubeletLiteral); $$ pairs left to
		// right as the kubelet reads it, so $$( is a literal and $$$( is not.
		`(!has(c.command) || c.command.all(x, !x.replace("$$", "").contains("$("))) && ` +
			`(!has(c.args) || c.args.all(a, !a.replace("$$", "").contains("$("))) && ` +
			`(!has(c.env) || c.env.all(e, !has(e.value) || !e.value.replace("$$", "").contains("$(")))`,
		`(!has(c.args) || c.args.all(a, !` + forms("forge-client-secret") + `)) && ` +
			`(!has(c.env) || c.env.all(e, e.name != "DOTVIRT_FORGE_CLIENT_SECRET" || (has(e.valueFrom) && has(e.valueFrom.secretKeyRef))))`,
		`(` + insecure + ` || (` + never("insecure-tls", "DOTVIRT_INSECURE_TLS") + `))`,
		never("kubeconfig", "KUBECONFIG"),
		never("static-dir", "DOTVIRT_STATIC_DIR"),
		`(!has(c.env) || c.env.all(e, !(e.name in ` + list(trustEnv...) + `)))`,
	}, " && ")
}

// consoleVolumes are the console's volume rule (over v) and mount rule (over
// m): each volume by its rendered name from the claim, ConfigMap or Secret
// rendered or an admin chose, mounted at its own path.
func consoleVolumes(p Params) (volumes, mounts string) {
	field := map[string]string{"persistentVolumeClaim": "claimName", "configMap": "name", "secret": "secretName"}
	var vs, ms []string
	for _, v := range ConsoleVolumes(p.BrandingConfigMap, p.MetricsCAConfigMap, p.OIDCCAConfigMap, p.ForgeSecret) {
		src := "v." + v.Kind
		vs = append(vs, `(v.name == `+quote(v.Name)+` && has(`+src+`) && has(`+src+`.`+field[v.Kind]+`) && `+src+`.`+field[v.Kind]+` == `+quote(v.Source)+`)`)
		ms = append(ms, `(m.name == `+quote(v.Name)+` && m.mountPath == `+quote(v.MountPath)+`)`)
	}
	return strings.Join(vs, " || "), strings.Join(ms, " || ")
}

// noExec is container c starting no process of its own: no exec lifecycle
// hook and no exec probe.
const noExec = `(!has(c.lifecycle) || ((!has(c.lifecycle.postStart) || !has(c.lifecycle.postStart.exec)) && ` +
	`(!has(c.lifecycle.preStop) || !has(c.lifecycle.preStop.exec)))) && ` +
	`!(has(c.livenessProbe) && has(c.livenessProbe.exec)) && !(has(c.readinessProbe) && has(c.readinessProbe.exec)) && ` +
	`!(has(c.startupProbe) && has(c.startupProbe.exec))`

// frontDoorRules are the front-door rules by kind: the console's Service,
// Route, Ingress and HTTPRoute (each named dotvirt in the install namespace)
// as rendered, and no other exposure on the console's host.
type frontDoorRules struct {
	services, routes, ingresses, httproutes []admissionregistrationv1.Validation
}

func frontDoor(p Params) frontDoorRules {
	console := `(object.metadata.name == ` + quote(ConsoleDeployment) + ` && request.namespace == ` + quote(p.Namespace) + `)`
	host := quote(p.ConsoleHost)
	// Annotations steer some controllers (an ingress controller's mirror or
	// snippet): the console's exposures keep whatever they have, and the
	// operator adds none. OpenShift's route API marks a host it generates
	// with one, on create, before admission sees the Route.
	generated := quote("openshift.io/host.generated")
	annotations, old := "object.metadata.annotations", "oldObject.metadata.annotations"
	sameAnnotations := `(request.operation == "CREATE" ? ` +
		`(!has(` + annotations + `) || ` + annotations + `.all(k, k == ` + generated + ` && ` + annotations + `[k] == "true")) : ` +
		`((!has(` + annotations + `) || ` + annotations + `.all(k, k == ` + generated + ` || (has(` + old + `) && k in ` + old + ` && ` + old + `[k] == ` + annotations + `[k]))) && ` +
		`(!has(` + old + `) || ` + old + `.all(k, k == ` + generated + ` || (has(` + annotations + `) && k in ` + annotations + `)))))`
	// A wildcard host serves the console's host too.
	covers := func(h string) string {
		return `(` + h + `.startsWith("*.") && ` + host + `.endsWith(` + h + `.substring(1)))`
	}
	// Every other exposure names its hosts, none the console's: one with none
	// (an Ingress default backend, an HTTPRoute without hostnames) answers for
	// every host its gateway or controller serves.
	const others = "the operator exposes nothing else on the console's host, and nothing on every host"
	return frontDoorRules{
		services: []admissionregistrationv1.Validation{
			// External IPs and load balancers take traffic for addresses the
			// cluster's own Services do not own.
			deny(`(!has(object.spec.type) || object.spec.type == "ClusterIP") && `+
				`(!has(object.spec.externalIPs) || size(object.spec.externalIPs) == 0) && `+
				`(!has(object.spec.loadBalancerIP) || object.spec.loadBalancerIP == "")`,
				"the operator exposes no Service outside the cluster: no node port, load balancer, external IP or external name"),
			deny(`!`+console+` || (`+
				`has(object.spec.selector) && object.spec.selector == {`+quote(ConsoleSelectorKey)+`: `+quote(ConsoleSelectorValue)+`} && `+
				`(!has(object.spec.type) || object.spec.type == "ClusterIP") && !has(object.spec.externalName) && `+
				`!(has(object.spec.publishNotReadyAddresses) && object.spec.publishNotReadyAddresses) && `+
				`object.spec.ports.all(port, has(port.targetPort) && string(port.targetPort) in ["8080", "http"]))`,
				"the operator points the console's Service only at the console's pods and port"),
		},
		routes: []admissionregistrationv1.Validation{
			deny(`!`+console+` || (`+sameAnnotations+` && `+
				`object.spec.to.kind == "Service" && object.spec.to.name == `+quote(ConsoleDeployment)+` && `+
				`(!has(object.spec.alternateBackends) || size(object.spec.alternateBackends) == 0) && `+
				`has(object.spec.port) && string(object.spec.port.targetPort) == "http" && `+
				`has(object.spec.tls) && object.spec.tls.termination == "edge" && !has(object.spec.tls.destinationCACertificate) && `+
				// An unknown host (no cluster ingress domain to read, as on
				// MicroShift) leaves the router's own host to the hostless Route.
				`(`+host+` == "" || !has(object.spec.host) || object.spec.host == "" || object.spec.host == `+host+`))`,
				"the operator routes the console's host only to the console's Service, edge-terminated"),
			deny(console+` || ((!has(object.spec.host) || object.spec.host == "" || object.spec.host != `+host+`) && `+
				`(!has(object.spec.wildcardPolicy) || object.spec.wildcardPolicy == "None"))`, others),
		},
		ingresses: []admissionregistrationv1.Validation{
			deny(`!`+console+` || (`+sameAnnotations+` && `+
				`(!has(object.spec.defaultBackend) || (has(object.spec.defaultBackend.service) && object.spec.defaultBackend.service.name == `+quote(ConsoleDeployment)+`)) && `+
				`object.spec.rules.all(r, has(r.host) && r.host == `+host+` && has(r.http) && r.http.paths.all(path, `+
				`has(path.backend.service) && path.backend.service.name == `+quote(ConsoleDeployment)+` && path.backend.service.port.number == 8080)))`,
				"the operator sends the console's Ingress only to the console's Service, on the console's host"),
			deny(console+` || (!has(object.spec.defaultBackend) && (!has(object.spec.rules) || `+
				`object.spec.rules.all(r, has(r.host) && r.host != "" && r.host != `+host+` && !`+covers("r.host")+`)))`, others),
		},
		httproutes: []admissionregistrationv1.Validation{
			deny(`!`+console+` || (`+sameAnnotations+` && `+
				`has(object.spec.hostnames) && object.spec.hostnames == [`+host+`] && object.spec.rules.all(r, !has(r.filters) && r.backendRefs.all(b, `+
				`!has(b.filters) && (!has(b.kind) || b.kind == "Service") && (!has(b.group) || b.group == "") && `+
				`(!has(b.namespace) || b.namespace == `+quote(p.Namespace)+`) && b.name == `+quote(ConsoleDeployment)+` && b.port == 8080)))`,
				"the operator sends the console's HTTPRoute only to the console's Service, on the console's host, unfiltered"),
			deny(console+` || (has(object.spec.hostnames) && size(object.spec.hostnames) > 0 && `+
				`object.spec.hostnames.all(h, h != `+host+` && !`+covers("h")+`))`, others),
		},
	}
}

// trustEnv are the env Go's HTTP clients take a proxy or a trust store from.
var trustEnv = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

// pod is the Deployment's pod spec in CEL.
const pod = "object.spec.template.spec"

// imageFrom is container c's image coming from repository (then a tag or a
// digest, so a lookalike name fails) or being one an admin chose. A choice
// inside repository is left out (rendered empty, never an image): the
// repository admits it already, and pinning its digest would make every
// release's new digest read Outdated.
func imageFrom(repository string, chosen ...string) string {
	var outside []string
	for _, img := range chosen {
		if InRepository(repository, img) {
			img = ""
		}
		outside = append(outside, img)
	}
	return `(c.image.startsWith(` + quote(repository+"@sha256:") + `) || c.image.startsWith(` + quote(repository+":") + `) || c.image in ` + list(outside...) + `)`
}

// InRepository is img coming from repository, as the guard's repository pin
// reads it: the repository, then a tag or a digest.
func InRepository(repository, img string) bool {
	return strings.HasPrefix(img, repository+":") || strings.HasPrefix(img, repository+"@sha256:")
}

// mountClear is mount m neither at or above the ServiceAccount token
// directory (under /var/run, or /run it may link to) nor inside it, and in
// a canonical form, so a doubled or dotted path cannot hide either.
var mountClear = func() string {
	var parts []string
	for _, dir := range []string{"/var/run/secrets/kubernetes.io/serviceaccount", "/run/secrets/kubernetes.io/serviceaccount"} {
		parts = append(parts,
			`!`+quote(dir+"/")+`.startsWith(m.mountPath.endsWith("/") ? m.mountPath : m.mountPath + "/")`,
			`!m.mountPath.startsWith(`+quote(dir+"/")+`)`)
	}
	parts = append(parts, `!m.mountPath.contains("//")`, `!m.mountPath.contains("/./")`, `!m.mountPath.contains("/../")`,
		`!m.mountPath.endsWith("/.")`, `!m.mountPath.endsWith("/..")`)
	return strings.Join(parts, " && ")
}()

// eachContainer holds rule (over c) for every container and init container.
func eachContainer(rule string) string {
	return "(" + pod + `.containers.all(c, ` + rule + `) && ` +
		`(!has(` + pod + `.initContainers) || ` + pod + `.initContainers.all(c, ` + rule + `)))`
}

// clusterBindings are the ClusterRoleBindings the operator renders, by name,
// with every subject any of its renders gives them.
func clusterBindings(p Params) map[string][]string {
	sa := func(ns, name string) string { return "ServiceAccount/" + ns + "/" + name }
	group := func(name string) string { return "Group//" + name }
	return map[string][]string{
		"dotvirt":                        {sa(p.Namespace, OperandAccounts[0])},
		"dotvirt-argocd-apply":           {sa(p.ArgoNamespace, p.ArgoController), sa(p.ArgoNamespace, PlatformSyncAccount)},
		"dotvirt-argocd-read":            {sa(p.ArgoNamespace, p.ArgoController)},
		PlatformSyncAccount:              {sa(p.ArgoNamespace, PlatformSyncAccount)},
		"dotvirt-platform-network-admin": {group("dotvirt-platform-admins")},
		"dotvirt-storage-catalog":        {group("system:authenticated")},
	}
}

// roleBindings are the RoleBindings the operator renders, in the ArgoCD
// namespace.
func roleBindings(p Params) map[string][]string {
	return map[string][]string{"dotvirt-sync-request": {"ServiceAccount/" + p.Namespace + "/" + OperandAccounts[0]}}
}

func subjectMap(p Params) string {
	all := clusterBindings(p)
	for k, v := range roleBindings(p) {
		all[k] = v
	}
	var entries []string
	for _, k := range slices.Sorted(mapKeys(all)) {
		entries = append(entries, quote(k)+": "+list(all[k]...))
	}
	return "{" + strings.Join(entries, ", ") + "}"
}

func mapKeys(m map[string][]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func rules(group, resource string) []admissionregistrationv1.NamedRuleWithOperations {
	return rule(group, "v1", resource, admissionregistrationv1.Create, admissionregistrationv1.Update)
}

func rule(group, version, resource string, ops ...admissionregistrationv1.OperationType) []admissionregistrationv1.NamedRuleWithOperations {
	return []admissionregistrationv1.NamedRuleWithOperations{{
		RuleWithOperations: admissionregistrationv1.RuleWithOperations{
			Operations: ops,
			Rule:       admissionregistrationv1.Rule{APIGroups: []string{group}, APIVersions: []string{version}, Resources: []string{resource}},
		},
	}}
}

// unchanged is the field at path in object equal to the one in oldObject,
// absent in both included.
func unchanged(path, oldPath string) string {
	return `has(` + path + `) == has(` + oldPath + `) && (!has(` + path + `) || ` + path + ` == ` + oldPath + `)`
}

func policy(name string, match []admissionregistrationv1.NamedRuleWithOperations, operator string, validations []admissionregistrationv1.Validation) *admissionregistrationv1.ValidatingAdmissionPolicy {
	return &admissionregistrationv1.ValidatingAdmissionPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: guardLabels()},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy:    ptr.To(admissionregistrationv1.Fail),
			MatchConstraints: &admissionregistrationv1.MatchResources{ResourceRules: match},
			MatchConditions:  []admissionregistrationv1.MatchCondition{{Name: "operator", Expression: operator}},
			Validations:      validations,
		},
	}
}

func deny(expression, message string) admissionregistrationv1.Validation {
	return admissionregistrationv1.Validation{
		Expression: expression,
		Message:    message + " (dotvirt operator guard)",
		Reason:     ptr.To(metav1.StatusReasonForbidden),
	}
}

func guardLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/name": "dotvirt", "app.kubernetes.io/part-of": "dotvirt-operator-guard"}
}

// quote is a CEL string literal; JSON's escaping is valid CEL.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// list is a CEL list of string literals.
func list(items ...string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func toUnstructured(o runtime.Object) *unstructured.Unstructured {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		panic(err) // the typed objects rendered here always convert
	}
	u := &unstructured.Unstructured{Object: m}
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "status")
	return u
}

func diffPolicy(want, live *unstructured.Unstructured) string {
	var w, h admissionregistrationv1.ValidatingAdmissionPolicy
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return "cannot be read as a ValidatingAdmissionPolicy"
	}
	switch {
	case h.Spec.FailurePolicy == nil || *h.Spec.FailurePolicy != *w.Spec.FailurePolicy:
		return "does not fail closed"
	case h.Spec.ParamKind != nil || !slices.EqualFunc(h.Spec.Variables, w.Spec.Variables, func(a, b admissionregistrationv1.Variable) bool { return a == b }):
		return "takes parameters or variables the render does not"
	case h.Spec.MatchConstraints == nil || !sameMatch(h.Spec.MatchConstraints, w.Spec.MatchConstraints):
		return "matches other requests than the render"
	case !slices.EqualFunc(h.Spec.MatchConditions, w.Spec.MatchConditions, func(a, b admissionregistrationv1.MatchCondition) bool {
		return a.Name == b.Name && a.Expression == b.Expression
	}):
		return "applies to another account than " + strings.TrimPrefix(w.Spec.MatchConditions[0].Expression, "request.userInfo.username == ")
	}
	have := map[string]bool{}
	for _, v := range h.Spec.Validations {
		have[v.Expression] = true
	}
	for _, v := range w.Spec.Validations {
		if !have[v.Expression] {
			return fmt.Sprintf("lacks the rule %q", strings.TrimSuffix(v.Message, " (dotvirt operator guard)"))
		}
	}
	if len(h.Spec.Validations) != len(w.Spec.Validations) {
		return "holds rules the render does not"
	}
	return ""
}

func diffBinding(want, live *unstructured.Unstructured) string {
	var w, h admissionregistrationv1.ValidatingAdmissionPolicyBinding
	if runtime.DefaultUnstructuredConverter.FromUnstructured(want.Object, &w) != nil ||
		runtime.DefaultUnstructuredConverter.FromUnstructured(live.Object, &h) != nil {
		return "cannot be read as a ValidatingAdmissionPolicyBinding"
	}
	switch {
	case h.Spec.PolicyName != w.Spec.PolicyName:
		return "enforces another policy"
	case !slices.Equal(h.Spec.ValidationActions, w.Spec.ValidationActions):
		return "does not only deny"
	case h.Spec.ParamRef != nil || (h.Spec.MatchResources != nil && (len(h.Spec.MatchResources.ResourceRules) > 0 ||
		len(h.Spec.MatchResources.ExcludeResourceRules) > 0 || !selectsAll(h.Spec.MatchResources))):
		return "is narrowed to fewer requests"
	}
	return ""
}

func sameMatch(h, w *admissionregistrationv1.MatchResources) bool {
	return len(h.ExcludeResourceRules) == 0 && selectsAll(h) &&
		slices.EqualFunc(h.ResourceRules, w.ResourceRules, func(x, y admissionregistrationv1.NamedRuleWithOperations) bool {
			return len(x.ResourceNames) == 0 && slices.Equal(x.Operations, y.Operations) &&
				slices.Equal(x.APIGroups, y.APIGroups) && slices.Equal(x.APIVersions, y.APIVersions) && slices.Equal(x.Resources, y.Resources)
		})
}

// selectsAll: the apiserver defaults both selectors to empty, which selects
// everything.
func selectsAll(m *admissionregistrationv1.MatchResources) bool {
	empty := func(s *metav1.LabelSelector) bool {
		return s == nil || (len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0)
	}
	return empty(m.NamespaceSelector) && empty(m.ObjectSelector)
}
