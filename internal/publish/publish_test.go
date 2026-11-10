package publish

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var settings = Settings{Domain: "apps.example.com", GatewayClass: "portail", ProjectLabel: "dotvirt.io/project"}

// Each listener takes *.<project>.<domain> and only HTTPRoutes from the
// project's namespaces; HTTPS and its certificate come only with an issuer.
func TestManifests(t *testing.T) {
	files, err := Manifests(settings, "shop", "web", false)
	if err != nil || len(files) != 1 || files[0].Path != "publishing/web/publish.yaml" {
		t.Fatalf("HTTP only: %+v %v", files, err)
	}
	var gw struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			GatewayClassName string `json:"gatewayClassName"`
			Listeners        []struct {
				Name, Hostname, Protocol string
				AllowedRoutes            struct {
					Namespaces struct {
						From     string
						Selector struct {
							MatchLabels map[string]string `json:"matchLabels"`
						}
					}
					Kinds []struct{ Kind string }
				} `json:"allowedRoutes"`
			}
		}
	}
	if err := yaml.Unmarshal(files[0].Content, &gw); err != nil {
		t.Fatal(err)
	}
	if gw.Metadata.Namespace != "web" || gw.Spec.GatewayClassName != "portail" || len(gw.Spec.Listeners) != 1 {
		t.Fatalf("Gateway = %+v", gw)
	}
	l := gw.Spec.Listeners[0]
	ns := l.AllowedRoutes.Namespaces
	if l.Hostname != "*.shop.apps.example.com" || ns.From != "Selector" || len(ns.Selector.MatchLabels) != 1 ||
		ns.Selector.MatchLabels["dotvirt.io/project"] != "shop" ||
		len(l.AllowedRoutes.Kinds) != 1 || l.AllowedRoutes.Kinds[0].Kind != "HTTPRoute" {
		t.Errorf("listener = %+v", l)
	}

	s := settings
	s.Issuer = "letsencrypt"
	files, err = Manifests(s, "shop", "web", false)
	if err != nil || len(files) != 2 || files[1].Path != "publishing/web/publish-tls.yaml" {
		t.Fatalf("with an issuer: %+v %v", files, err)
	}
	cert := string(files[1].Content)
	for _, want := range []string{"secretName: publish-tls", "- '*.shop.apps.example.com'", "kind: ClusterIssuer", "name: letsencrypt"} {
		if !strings.Contains(cert, want) {
			t.Errorf("Certificate lacks %q:\n%s", want, cert)
		}
	}
}

// On a VM Network the Gateway polls through the apiserver: it takes
// parameters saying so, and its pods' account is bound to portail's proxy
// Role in portail's namespace, and nowhere else.
func TestManifestsProxied(t *testing.T) {
	s := settings
	s.PortailNamespace = "portail-system"
	files, err := Manifests(s, "vpc", "web", true)
	if err != nil || len(files) != 3 {
		t.Fatalf("proxied: %+v %v", files, err)
	}
	if !strings.Contains(string(files[0].Content), "name: publish-parameters") {
		t.Errorf("Gateway names no parameters:\n%s", files[0].Content)
	}
	if files[1].Path != "publishing/web/publish-parameters.yaml" || !strings.Contains(string(files[1].Content), "controllerTransport: APIServerProxy") {
		t.Errorf("parameters = %s\n%s", files[1].Path, files[1].Content)
	}
	rb := string(files[2].Content)
	if files[2].Path != "rbac/portail-system/publish-vpc.yaml" {
		t.Errorf("binding path = %s", files[2].Path)
	}
	for _, want := range []string{"namespace: portail-system", "kind: Role", "name: portail-controller-proxy", "name: portail-publish", "namespace: web"} {
		if !strings.Contains(rb, want) {
			t.Errorf("binding lacks %q:\n%s", want, rb)
		}
	}
	if _, err := Manifests(settings, "vpc", "web", true); err == nil {
		t.Error("proxied without portail's namespace: rendered")
	}
}

func TestManifestsRefuse(t *testing.T) {
	long := settings
	long.Domain = strings.Repeat("a.", 123) + "com"
	bad := settings
	bad.Domain = "Apps_Example"
	nolabel := settings
	nolabel.ProjectLabel = ""
	for name, c := range map[string]struct {
		s           Settings
		project, ns string
	}{
		"off":           {Settings{GatewayClass: "portail", ProjectLabel: "p"}, "shop", "web"},
		"bad domain":    {bad, "shop", "web"},
		"bad project":   {settings, "Shop", "web"},
		"bad ns":        {settings, "shop", "Web"},
		"no label":      {nolabel, "shop", "web"},
		"host too long": {long, "shop", "web"},
	} {
		if _, err := Manifests(c.s, c.project, c.ns, false); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}
