package adminobjects

import (
	"net"
	"net/url"
	"path"
	"strings"
)

// What the operator renders and the guard pins alike, so neither drifts from
// the other.

// ConsoleAccount and ForgeAccount are the ServiceAccounts the console and the
// managed forge run as; ConsoleDeployment and ForgeDeployment their
// Deployments.
const (
	ConsoleAccount    = "dotvirt"
	ForgeAccount      = "dotvirt-forgejo"
	ConsoleDeployment = "dotvirt"
	ForgeDeployment   = "dotvirt-forgejo"
)

// ConsoleSelectorKey and ConsoleSelectorValue select the console's pods for
// its Service.
const (
	ConsoleSelectorKey   = "app"
	ConsoleSelectorValue = "dotvirt"
)

// InstallNamespaceEnv is the operator Deployment's env by which a cluster
// admin names the install's namespace.
const InstallNamespaceEnv = "OPERATOR_INSTALL_NAMESPACE"

// ArgoTarget is the ArgoCD namespace and controller ServiceAccount an install
// writes into: the spec's, else the defaults. The operator and the console
// resolve it alike, so the guard each renders pins the same accounts.
func ArgoTarget(specNamespace, specController string, openShift bool) (namespace, controller string) {
	namespace, controller = specNamespace, specController
	if namespace == "" {
		if openShift {
			namespace = "openshift-gitops"
		} else {
			namespace = "argocd"
		}
	}
	if controller == "" {
		// By the namespace, not the platform: MicroShift reads as OpenShift
		// and runs community Argo CD.
		if namespace == "openshift-gitops" {
			controller = "openshift-gitops-argocd-application-controller"
		} else {
			controller = "argocd-application-controller"
		}
	}
	return namespace, controller
}

// The console's metrics CA paths: the admin's spec.metrics.ca, mounted, or
// OpenShift's service CA for an in-cluster https querier.
const (
	MetricsCAPath = "/var/run/dotvirt/tls-metrics/ca.crt"
	ServiceCAPath = "/var/run/dotvirt/tls-service/service-ca.crt"
)

// IngressCAPath is the router CA the console trusts for OpenShift's OAuth
// server, mounted when SSO is on.
const IngressCAPath = "/var/run/dotvirt/tls-ingress/ca-bundle.crt"

// Metrics is the console's metrics configuration as rendered from an
// admin's spec.metrics: where every user's query goes with the user's token,
// and the CA it trusts there; "" when unset. The guard pins these, so only an
// admin chooses where the tokens go.
type Metrics struct {
	URL, TenancyURL, CA string
}

// MetricsFor renders spec.metrics: its url, its tenancyURL (else OpenShift's
// thanos-querier tenancy port beside 9091), and caSet whether spec.metrics.ca
// names a ConfigMap.
func MetricsFor(queryURL, tenancyURL string, caSet bool) Metrics {
	if queryURL == "" {
		return Metrics{}
	}
	m := Metrics{URL: queryURL, TenancyURL: tenancyURL}
	if m.TenancyURL == "" {
		m.TenancyURL = thanosTenancy(queryURL)
	}
	switch {
	case caSet:
		m.CA = MetricsCAPath
	case strings.HasPrefix(queryURL, "https://") && strings.Contains(queryURL, ".svc"):
		m.CA = ServiceCAPath
	}
	return m
}

// thanosTenancy is OpenShift's thanos-querier tenancy port for its 9091
// query port, "" for any other URL.
func thanosTenancy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Port() != "9091" {
		return ""
	}
	if h := u.Hostname(); h != "thanos-querier.openshift-monitoring.svc" && h != "thanos-querier.openshift-monitoring.svc.cluster.local" {
		return ""
	}
	u.Host = net.JoinHostPort(u.Hostname(), "9092")
	return u.String()
}

// SSOClientName is the OpenShift OAuthClient an install in namespace
// registers as: cluster-scoped, so the name carries the namespace.
func SSOClientName(namespace string) string { return "dotvirt-" + namespace }

// SSORedirect is where OpenShift's OAuth server sends an install's login
// codes: its console host, spec.ingress.host or else the host OpenShift's
// router gives the hostless Route named dotvirt in namespace on appsDomain.
// The console registers it from these admin-read values, never from the
// public URL the operator writes into its env: a redirect of the operator's
// choosing would receive every login code, and the operator holds the
// client secret.
func SSORedirect(specHost, namespace, appsDomain string) string {
	return "https://" + ConsoleHost(specHost, namespace, appsDomain, "") + "/api/auth/callback"
}

// ConsoleHost is the console's host: spec.ingress.host, else the host
// OpenShift's router gives the hostless Route named dotvirt in namespace on
// appsDomain, else the one host routers report admitting that Route under
// (routerHost, RouterHost: where no domain can be read, as on MicroShift),
// else "" (no router, no host: nothing is exposed).
func ConsoleHost(specHost, namespace, appsDomain, routerHost string) string {
	switch {
	case specHost != "":
		return specHost
	case appsDomain != "":
		return ConsoleDeployment + "-" + namespace + "." + appsDomain
	}
	return routerHost
}

// RouterHost is the one host a Route's status reports routers serving it
// under, "" for none or several. Routers write that status: the operator
// holds no right on routes/status.
func RouterHost(route map[string]any) string {
	status, _ := route["status"].(map[string]any)
	ingress, _ := status["ingress"].([]any)
	host := ""
	for _, i := range ingress {
		h, _ := i.(map[string]any)["host"].(string)
		switch {
		case h == "" || h == host:
		case host == "":
			host = h
		default:
			return ""
		}
	}
	return host
}

// ConsoleVolume is one of the console's volumes as rendered: where the
// console mounts it, and the claim, ConfigMap or Secret it takes.
type ConsoleVolume struct {
	Name, MountPath string
	// Kind is the volume source's field: persistentVolumeClaim, configMap or
	// secret.
	Kind, Source string
}

// DraftsClaim and the CA ConfigMaps are the console's own; the branding and
// metrics CA ConfigMaps and the forge Secret are the admin's choice.
const (
	DraftsClaim        = "dotvirt-drafts"
	IngressCAConfigMap = "dotvirt-ingress-ca"
	ServiceCAConfigMap = "dotvirt-service-ca"
	DefaultForgeSecret = "dotvirt-forge"
	BrandingMountPath  = "/etc/dotvirt/branding"
	ForgeMountPath     = "/var/run/dotvirt/forge"
)

// ConsoleVolumes are every volume the console may have, each mounted at its
// path only; an empty source name is a volume the install does not ask for.
func ConsoleVolumes(brandingConfigMap, metricsCAConfigMap, forgeSecret string) []ConsoleVolume {
	return []ConsoleVolume{
		{"drafts", "/var/lib/dotvirt/drafts", "persistentVolumeClaim", DraftsClaim},
		{"ingress-ca", path.Dir(IngressCAPath), "configMap", IngressCAConfigMap},
		{"service-ca", path.Dir(ServiceCAPath), "configMap", ServiceCAConfigMap},
		{"branding", BrandingMountPath, "configMap", brandingConfigMap},
		{"metrics-ca", path.Dir(MetricsCAPath), "configMap", metricsCAConfigMap},
		{"forge-token", ForgeMountPath, "secret", forgeSecret},
	}
}

// ForgeSecret is the install's forge credential Secret: spec.forge's
// credentialsSecret, else the one the managed forge's bootstrap writes.
func ForgeSecret(credentialsSecret string) string {
	if credentialsSecret != "" {
		return credentialsSecret
	}
	return DefaultForgeSecret
}

// ForgeAdminSecret is the only Secret the forge's pod reads: the bot
// account's password its bootstrap sets. ForgeClaim is the only claim it
// mounts.
const (
	ForgeAdminSecret = "dotvirt-forgejo-admin"
	ForgeClaim       = "dotvirt-forgejo-data"
)

// The console's own Secrets, which the operator generates.
const (
	SessionSecret = "dotvirt-session"
	OAuthSecret   = "dotvirt-oauth"
	AppsetSecret  = "dotvirt-appset-plugin"
	WebhookSecret = "dotvirt-webhook"
)

// ConsoleSecretEnv are the env the console takes from Secrets, as rendered,
// each as its env name, Secret and key: its own Secrets, the forge
// credential (ForgeSecret), and the OIDC and forge application client
// secrets an admin chose.
func ConsoleSecretEnv(p Params) [][3]string {
	return [][3]string{
		{"DOTVIRT_SESSION_SECRET", SessionSecret, "secret"},
		{"DOTVIRT_OAUTH_CLIENT_SECRET", OAuthSecret, "clientSecret"},
		{"DOTVIRT_APPSET_PLUGIN_TOKEN", AppsetSecret, "token"},
		{"DOTVIRT_WEBHOOK_SECRET", WebhookSecret, "secret"},
		{"DOTVIRT_GIT_USERNAME", p.ForgeSecret, "username"},
		{"DOTVIRT_FORGE_URL", p.ForgeSecret, "url"},
		{"DOTVIRT_OIDC_CLIENT_SECRET", p.OIDCSecret, p.OIDCSecretKey},
		{"DOTVIRT_FORGE_CLIENT_SECRET", p.ForgeClientSecret, p.ForgeClientSecretKey},
	}
}

// KubeletLiteral is s as a container's env value or argument that the kubelet
// hands the process unchanged: it expands $(NAME) there and turns $$ into $,
// so every $ is doubled.
func KubeletLiteral(s string) string { return strings.ReplaceAll(s, "$", "$$") }
