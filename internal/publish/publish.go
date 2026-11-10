// Package publish renders what turning publishing on stages into the platform
// repo for a project namespace: its Gateway and, with an issuer, the wildcard
// certificate the Gateway's HTTPS listener serves.
//
// The Gateway sits in the namespace it publishes. A namespace's VMs may be
// on its own primary network, which only a gateway joined to it reaches, and
// a Gateway per namespace owns its hostnames by construction: its listeners
// take only *.<namespace>.<domain>, and only routes from that namespace.
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
	// Issuer is a cert-manager ClusterIssuer able to issue each namespace's
	// wildcard (DNS-01); "" serves HTTP only.
	Issuer string
}

// On reports whether the install offers publishing at all.
func (s Settings) On() bool { return s.Domain != "" && s.GatewayClass != "" }

// Gateway names each namespace's Gateway; Certificate names its certificate
// and the Secret that certificate fills.
const (
	Gateway     = "publish"
	Certificate = "publish-tls"
)

// Hostname is ns's listener hostname: *.<ns>.<domain>.
func Hostname(ns, domain string) string { return "*." + ns + "." + domain }

// File is one platform-repo file publishing stages.
type File struct {
	Kind    model.Kind
	Name    string
	Path    string
	Content []byte
}

// Manifests renders ns's Gateway, then its Certificate when s names an issuer.
func Manifests(s Settings, ns string) ([]File, error) {
	if !s.On() {
		return nil, errors.New("publishing needs a domain and a GatewayClass")
	}
	if err := validate.RequireDNS1123("namespace", ns); err != nil {
		return nil, err
	}
	if errs := validation.IsDNS1123Subdomain(s.Domain); len(errs) > 0 {
		return nil, fmt.Errorf("publish domain %q: %s", s.Domain, strings.Join(errs, "; "))
	}
	host := Hostname(ns, s.Domain)
	if errs := validation.IsWildcardDNS1123Subdomain(host); len(errs) > 0 {
		return nil, fmt.Errorf("hostname %q: %s", host, strings.Join(errs, "; "))
	}
	// Same namespace and HTTPRoute only: another namespace's route, or a TCP
	// or TLS route, never attaches here.
	allowed := map[string]any{
		"namespaces": map[string]any{"from": "Same"},
		"kinds":      []any{map[string]any{"kind": "HTTPRoute"}},
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
	gw, err := render(model.KindGateway, ns, Gateway, map[string]any{
		"gatewayClassName": s.GatewayClass,
		"listeners":        listeners,
	})
	if err != nil {
		return nil, err
	}
	files := []File{gw}
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
