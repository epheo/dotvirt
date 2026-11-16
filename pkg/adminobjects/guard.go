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
	GuardDeploymentsName = "dotvirt-operator-deployments"
	GuardBindingsName    = "dotvirt-operator-bindings"
	GuardInstallName     = "dotvirt-operator-install"
	GuardFrontDoorName   = "dotvirt-operator-frontdoor"
)

// GuardNames are the guard's policy and binding names, in render order.
var GuardNames = []string{GuardSecretsName, GuardDeploymentsName, GuardBindingsName, GuardInstallName, GuardFrontDoorName}

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
	return guard(p, strconv.FormatBool(p.InsecureTLS), p.Metrics)
}

// guard renders with insecure, the CEL literal for p.InsecureTLS, which the
// release template leaves a placeholder.
func guard(p Params, insecure string, metrics Metrics) []Object {
	operator := `request.userInfo.username == ` + quote(p.Operator)
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
			deny(`object.metadata.name != `+quote(ConsoleDeployment)+` || `+pod+`.containers.all(c, `+consoleSettings(metrics, insecure)+`)`,
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
		policy(GuardFrontDoorName, append(append(append(rules("", "services"), rules("route.openshift.io", "routes")...),
			rules("networking.k8s.io", "ingresses")...), rules("gateway.networking.k8s.io", "httproutes")...), operator,
			frontDoor(p)),
	}
	purpose := map[string]string{
		GuardSecretsName:     "Bounds the operator's Secrets: none outside " + p.Namespace + " and " + p.ArgoNamespace + ", never a ServiceAccount token",
		GuardDeploymentsName: "Bounds the operator's Deployments: the console and the forge only, in " + p.Namespace + ", with their own accounts, images, commands and token, unprivileged and off the host",
		GuardBindingsName:    "Bounds the operator's RBAC bindings: only the ones it renders, to their own roles and rendered subjects",
		GuardInstallName:     "Bounds the operator's writes to its install: its finalizers only, never the spec an admin wrote",
		GuardFrontDoorName:   "Bounds the console's front door: its Service and exposure lead to the console on the host an admin chose",
	}
	var out []Object
	for _, pol := range policies {
		out = append(out, Object{Purpose: purpose[pol.Name], Object: toUnstructured(pol)})
	}
	for _, name := range GuardNames {
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
func consoleSettings(m Metrics, insecure string) string {
	forms := func(flag string) string {
		return `(a == ` + quote("-"+flag) + ` || a == ` + quote("--"+flag) +
			` || a.startsWith(` + quote("-"+flag+"=") + `) || a.startsWith(` + quote("--"+flag+"=") + `))`
	}
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
		`(` + insecure + ` || (` + never("insecure-tls", "DOTVIRT_INSECURE_TLS") + `))`,
		never("kubeconfig", "KUBECONFIG"),
		never("static-dir", "DOTVIRT_STATIC_DIR"),
		`(!has(c.env) || c.env.all(e, !(e.name in ` + list(trustEnv...) + `)))`,
	}, " && ")
}

// noExec is container c starting no process of its own: no exec lifecycle
// hook and no exec probe.
const noExec = `(!has(c.lifecycle) || ((!has(c.lifecycle.postStart) || !has(c.lifecycle.postStart.exec)) && ` +
	`(!has(c.lifecycle.preStop) || !has(c.lifecycle.preStop.exec)))) && ` +
	`!(has(c.livenessProbe) && has(c.livenessProbe.exec)) && !(has(c.readinessProbe) && has(c.readinessProbe.exec)) && ` +
	`!(has(c.startupProbe) && has(c.startupProbe.exec))`

// frontDoor are the front-door rules: the console's Service, Route, Ingress
// and HTTPRoute (each named dotvirt in the install namespace) as rendered,
// and no other exposure on the console's host.
func frontDoor(p Params) []admissionregistrationv1.Validation {
	kind := func(resource string) string { return `request.resource.resource == ` + quote(resource) }
	console := `(object.metadata.name == ` + quote(ConsoleDeployment) + ` && request.namespace == ` + quote(p.Namespace) + `)`
	host := quote(p.ConsoleHost)
	// Annotations steer some controllers (an ingress controller's mirror or
	// snippet): the console's exposures keep whatever they have, and the
	// operator adds none.
	sameAnnotations := `(request.operation == "CREATE" ? !has(object.metadata.annotations) : ` +
		unchanged("object.metadata.annotations", "oldObject.metadata.annotations") + `)`
	return []admissionregistrationv1.Validation{
		deny(`!(`+kind("services")+` && `+console+`) || (`+
			`has(object.spec.selector) && object.spec.selector == {`+quote(ConsoleSelectorKey)+`: `+quote(ConsoleSelectorValue)+`} && `+
			`(!has(object.spec.type) || object.spec.type == "ClusterIP") && !has(object.spec.externalName) && `+
			`!(has(object.spec.publishNotReadyAddresses) && object.spec.publishNotReadyAddresses) && `+
			`object.spec.ports.all(port, has(port.targetPort) && string(port.targetPort) in ["8080", "http"]))`,
			"the operator points the console's Service only at the console's pods and port"),
		deny(`!(`+kind("routes")+` && `+console+`) || (`+sameAnnotations+` && `+
			`object.spec.to.kind == "Service" && object.spec.to.name == `+quote(ConsoleDeployment)+` && `+
			`(!has(object.spec.alternateBackends) || size(object.spec.alternateBackends) == 0) && `+
			`has(object.spec.port) && string(object.spec.port.targetPort) == "http" && `+
			`has(object.spec.tls) && object.spec.tls.termination == "edge" && !has(object.spec.tls.destinationCACertificate) && `+
			`(!has(object.spec.host) || object.spec.host == "" || object.spec.host == `+host+`))`,
			"the operator routes the console's host only to the console's Service, edge-terminated"),
		deny(`!(`+kind("ingresses")+` && `+console+`) || (`+sameAnnotations+` && `+
			`(!has(object.spec.defaultBackend) || (has(object.spec.defaultBackend.service) && object.spec.defaultBackend.service.name == `+quote(ConsoleDeployment)+`)) && `+
			`object.spec.rules.all(r, has(r.host) && r.host == `+host+` && has(r.http) && r.http.paths.all(path, `+
			`has(path.backend.service) && path.backend.service.name == `+quote(ConsoleDeployment)+` && path.backend.service.port.number == 8080)))`,
			"the operator sends the console's Ingress only to the console's Service, on the console's host"),
		deny(`!(`+kind("httproutes")+` && `+console+`) || (`+sameAnnotations+` && `+
			`object.spec.hostnames == [`+host+`] && object.spec.rules.all(r, !has(r.filters) && r.backendRefs.all(b, `+
			`!has(b.filters) && (!has(b.kind) || b.kind == "Service") && (!has(b.group) || b.group == "") && `+
			`(!has(b.namespace) || b.namespace == `+quote(p.Namespace)+`) && b.name == `+quote(ConsoleDeployment)+` && b.port == 8080)))`,
			"the operator sends the console's HTTPRoute only to the console's Service, on the console's host, unfiltered"),
		deny(``+console+` || `+host+` == "" || (`+
			`(!(`+kind("routes")+`) || ((!has(object.spec.host) || object.spec.host != `+host+`) && (!has(object.spec.wildcardPolicy) || object.spec.wildcardPolicy == "None"))) && `+
			`(!(`+kind("ingresses")+`) || !has(object.spec.rules) || object.spec.rules.all(r, !has(r.host) || r.host != `+host+`)) && `+
			`(!(`+kind("httproutes")+`) || !has(object.spec.hostnames) || !(`+host+` in object.spec.hostnames)))`,
			"the operator exposes nothing else on the console's host"),
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
// digest, so a lookalike name fails) or being one an admin chose.
func imageFrom(repository string, chosen ...string) string {
	return `(c.image.startsWith(` + quote(repository+"@sha256:") + `) || c.image.startsWith(` + quote(repository+":") + `) || c.image in ` + list(chosen...) + `)`
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
