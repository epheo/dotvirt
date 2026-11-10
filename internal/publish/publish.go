// Package publish renders what turning publishing on stages into the platform
// repo for a project: its Gateway and, with an issuer, the wildcard
// certificate the Gateway's HTTPS listener serves.
//
// The Gateway sits in one project namespace and serves the others on its
// network: a primary network is reached only by a gateway joined to it. A
// Gateway per project owns its hostnames by construction: its listeners take
// only *.<project>.<domain>, and only routes from namespaces labeled with the
// project, a label no tenant role may change.
package publish

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// Settings are the platform's: an install publishes under one domain, through
// one GatewayClass, with one issuer.
type Settings struct {
	Domain       string
	GatewayClass string
	// Issuer is a cert-manager ClusterIssuer able to issue each project's
	// wildcard (DNS-01); "" serves HTTP only.
	Issuer string
	// ProjectLabel is the namespace label naming a project.
	ProjectLabel string
	// PortailNamespace holds portail's controller and the Role letting a
	// Gateway's pods poll it through the apiserver (ProxyRole).
	PortailNamespace string
}

// On reports whether the install offers publishing at all.
func (s Settings) On() bool { return s.Domain != "" && s.GatewayClass != "" }

// Gateway names each project's Gateway; Certificate names its certificate
// and the Secret that certificate fills; Parameters its GatewayParameters.
const (
	Gateway     = "publish"
	Certificate = "publish-tls"
	Parameters  = "publish-parameters"
)

// ProxyRole is portail's Role granting services/proxy on its controller's
// sync port, and only that (deploy/components/apiserver-proxy).
const ProxyRole = "portail-controller-proxy"

// ServiceAccount is the account portail runs Gateway's pods as.
const ServiceAccount = "portail-" + Gateway

// Hostname is a project's listener hostname: *.<project>.<domain>.
func Hostname(project, domain string) string { return "*." + project + "." + domain }

// File is one platform-repo file publishing stages.
type File struct {
	Kind    model.Kind
	Name    string
	Path    string
	Content []byte
}

// Manifests renders project's Gateway in ns, then its Certificate when s
// names an issuer. proxied: ns is on a VM Network, whose pods reach portail's
// controller only through the apiserver; the Gateway then takes parameters
// saying so, and a RoleBinding in PortailNamespace lets its pods.
func Manifests(s Settings, project, ns string, proxied bool) ([]File, error) {
	if !s.On() {
		return nil, errors.New("publishing needs a domain and a GatewayClass")
	}
	if err := validate.RequireDNS1123("project", project); err != nil {
		return nil, err
	}
	if err := validate.RequireDNS1123("namespace", ns); err != nil {
		return nil, err
	}
	if err := validate.RequireLabelKey("project label", s.ProjectLabel); err != nil {
		return nil, err
	}
	if errs := validation.IsDNS1123Subdomain(s.Domain); len(errs) > 0 {
		return nil, fmt.Errorf("publish domain %q: %s", s.Domain, strings.Join(errs, "; "))
	}
	host := Hostname(project, s.Domain)
	if errs := validation.IsWildcardDNS1123Subdomain(host); len(errs) > 0 {
		return nil, fmt.Errorf("hostname %q: %s", host, strings.Join(errs, "; "))
	}
	// The project's namespaces and HTTPRoute only: another project's route,
	// or a TCP or TLS route, never attaches here.
	allowed := map[string]any{
		"namespaces": map[string]any{
			"from":     "Selector",
			"selector": map[string]any{"matchLabels": map[string]any{s.ProjectLabel: project}},
		},
		"kinds": []any{map[string]any{"kind": "HTTPRoute"}},
	}
	listeners := []any{map[string]any{
		"name": "http", "protocol": "HTTP", "port": 80, "hostname": host, "allowedRoutes": allowed,
	}}
	if s.Issuer != "" {
		listeners = append(listeners, map[string]any{
			"name": "https", "protocol": "HTTPS", "port": 443, "hostname": host, "allowedRoutes": allowed,
			"tls": map[string]any{
				"mode":            "Terminate",
				"certificateRefs": []any{map[string]any{"kind": "Secret", "name": Certificate}},
			},
		})
	}
	spec := map[string]any{"gatewayClassName": s.GatewayClass, "listeners": listeners}
	if proxied {
		spec["infrastructure"] = map[string]any{"parametersRef": map[string]any{
			"group": model.KindGWParams.Group, "kind": model.KindGWParams.Kind, "name": Parameters,
		}}
	}
	gw, err := render(model.KindGateway, ns, Gateway, spec)
	if err != nil {
		return nil, err
	}
	files := []File{gw}
	if proxied {
		more, err := proxyFiles(s, project, ns)
		if err != nil {
			return nil, err
		}
		files = append(files, more...)
	}
	if s.Issuer == "" {
		return files, nil
	}
	cert, err := render(model.KindCert, ns, Certificate, map[string]any{
		"secretName": Certificate,
		"dnsNames":   []any{host},
		"issuerRef":  map[string]any{"group": "cert-manager.io", "kind": "ClusterIssuer", "name": s.Issuer},
	})
	if err != nil {
		return nil, err
	}
	return append(files, cert), nil
}

// proxyFiles are the parameters sending ns's Gateway through the apiserver,
// and the binding letting its pods do it.
func proxyFiles(s Settings, project, ns string) ([]File, error) {
	if err := validate.RequireDNS1123("portail namespace", s.PortailNamespace); err != nil {
		return nil, err
	}
	params, err := render(model.KindGWParams, ns, Parameters, map[string]any{"controllerTransport": "APIServerProxy"})
	if err != nil {
		return nil, err
	}
	name := ProxyBinding(project)
	rb, err := yaml.Marshal(map[string]any{
		"apiVersion": model.KindRB.APIVersion(),
		"kind":       model.KindRB.Kind,
		"metadata":   map[string]any{"name": name, "namespace": s.PortailNamespace},
		"roleRef":    map[string]any{"apiGroup": model.KindRB.Group, "kind": "Role", "name": ProxyRole},
		"subjects":   []any{map[string]any{"kind": "ServiceAccount", "name": ServiceAccount, "namespace": ns}},
	})
	if err != nil {
		return nil, err
	}
	return []File{params, {Kind: model.KindRB, Name: name, Path: model.KindRB.RepoPath(s.PortailNamespace, name), Content: rb}}, nil
}

// ProxyBinding names project's RoleBinding in portail's namespace.
func ProxyBinding(project string) string { return "publish-" + project }

func render(k model.Kind, ns, name string, spec map[string]any) (File, error) {
	content, err := yaml.Marshal(map[string]any{
		"apiVersion": k.APIVersion(),
		"kind":       k.Kind,
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	})
	if err != nil {
		return File{}, err
	}
	return File{Kind: k, Name: name, Path: k.RepoPath(ns, name), Content: content}, nil
}
