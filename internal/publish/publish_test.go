package publish

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Each listener takes *.<namespace>.<domain> and only that namespace's
// HTTPRoutes; HTTPS and its certificate come only with an issuer.
func TestManifests(t *testing.T) {
	s := Settings{Domain: "apps.example.com", GatewayClass: "portail"}
	files, err := Manifests(s, "web")
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
					Namespaces struct{ From string }
					Kinds      []struct{ Kind string }
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
	if l.Hostname != "*.web.apps.example.com" || l.AllowedRoutes.Namespaces.From != "Same" ||
		len(l.AllowedRoutes.Kinds) != 1 || l.AllowedRoutes.Kinds[0].Kind != "HTTPRoute" {
		t.Errorf("listener = %+v", l)
	}

	s.Issuer = "letsencrypt"
	files, err = Manifests(s, "web")
	if err != nil || len(files) != 2 || files[1].Path != "publishing/web/publish-tls.yaml" {
		t.Fatalf("with an issuer: %+v %v", files, err)
	}
	cert := string(files[1].Content)
	for _, want := range []string{"secretName: publish-tls", "- '*.web.apps.example.com'", "kind: ClusterIssuer", "name: letsencrypt"} {
		if !strings.Contains(cert, want) {
			t.Errorf("Certificate lacks %q:\n%s", want, cert)
		}
	}
}

func TestManifestsRefuse(t *testing.T) {
	for name, c := range map[string]struct {
		s  Settings
		ns string
	}{
		"off":        {Settings{GatewayClass: "portail"}, "web"},
		"bad domain": {Settings{Domain: "Apps_Example", GatewayClass: "portail"}, "web"},
		"bad ns":     {Settings{Domain: "apps.example.com", GatewayClass: "portail"}, "Web"},
		"too long":   {Settings{Domain: strings.Repeat("a.", 123) + "com", GatewayClass: "portail"}, "web"},
	} {
		if _, err := Manifests(c.s, c.ns); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}
