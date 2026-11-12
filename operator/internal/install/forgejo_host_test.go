package install

import (
	"testing"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A spec.forge.url carrying a port (the in-cluster service URL an e2e or
// air-gapped install uses) must still yield a VALID Route host: bare hostname,
// no port — the apiserver rejects "host:port" and ForgeReady wedges on it.
func TestForgejoHostStripsPort(t *testing.T) {
	dv := func(url string) *dotvirtv1alpha1.Dotvirt {
		return &dotvirtv1alpha1.Dotvirt{
			ObjectMeta: metav1.ObjectMeta{Name: "dotvirt", Namespace: "dotvirt"},
			Spec:       dotvirtv1alpha1.DotvirtSpec{Forge: dotvirtv1alpha1.ForgeSpec{URL: url}},
		}
	}
	cases := map[string]string{
		"http://dotvirt-forgejo.dotvirt.svc.cluster.local:3000": "dotvirt-forgejo.dotvirt.svc.cluster.local",
		"https://forgejo.apps.example.com":                      "forgejo.apps.example.com",
		"":                                                      "",
	}
	for url, want := range cases {
		if got := ForgejoHost(dv(url)); got != want {
			t.Errorf("ForgejoHost(%q) = %q, want %q", url, got, want)
		}
	}
}

// Forgejo's ROOT_URL is where browsers open it: webURL when set, so the pages
// served at that host carry no wrong-ROOT_URL banner and clone links name it.
func TestForgejoRootURLFollowsTheWebURL(t *testing.T) {
	dv := &dotvirtv1alpha1.Dotvirt{
		ObjectMeta: metav1.ObjectMeta{Name: "dotvirt", Namespace: "dotvirt"},
		Spec: dotvirtv1alpha1.DotvirtSpec{Forge: dotvirtv1alpha1.ForgeSpec{
			Managed: true,
			URL:     "http://dotvirt-forgejo.dotvirt.svc.cluster.local:3000",
		}},
	}
	root := func() string {
		v, _ := envValue(ForgejoDeployment(dv, false, "", "h").Spec.Template.Spec.Containers[0].Env, "FORGEJO__server__ROOT_URL")
		return v
	}
	if got := root(); got != "http://dotvirt-forgejo.dotvirt.svc.cluster.local:3000/" {
		t.Errorf("no webURL: ROOT_URL = %q, want the forge's URL", got)
	}
	dv.Spec.Forge.WebURL = "https://forge.apps.example.com/"
	if got := root(); got != "https://forge.apps.example.com/" {
		t.Errorf("webURL set: ROOT_URL = %q, want the webURL", got)
	}
}
