package adminobjects

import (
	"net"
	"net/url"
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
	return "https://" + ConsoleHost(specHost, namespace, appsDomain) + "/api/auth/callback"
}

// ConsoleHost is the console's host: spec.ingress.host, else the host
// OpenShift's router gives the hostless Route named dotvirt in namespace on
// appsDomain, else "" (no router, no host: nothing is exposed).
func ConsoleHost(specHost, namespace, appsDomain string) string {
	switch {
	case specHost != "":
		return specHost
	case appsDomain != "":
		return ConsoleDeployment + "-" + namespace + "." + appsDomain
	}
	return ""
}
