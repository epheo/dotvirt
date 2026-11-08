package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// grant is one RBAC rule as the fake apiserver weighs it: an empty namespace
// or name covers every one, as a cluster-wide rule without resourceNames does.
type grant struct{ verb, group, resource, sub, ns, name string }

func (g grant) covers(a *authzv1.ResourceAttributes) bool {
	return g.verb == a.Verb && g.group == a.Group && g.resource == a.Resource && g.sub == a.Subresource &&
		(g.ns == "" || g.ns == a.Namespace) && (g.name == "" || g.name == a.Name)
}

// gateRig is one server whose SSARs answer from per-token grants: each
// verdict a case needs is asked under a fresh token, so no cached verdict
// crosses grant sets.
type gateRig struct {
	t      *testing.T
	fc     *fakeCluster
	d      *gatedDraft
	s      *Server
	mu     sync.Mutex
	grants map[string][]grant
	n      int
}

const descheduler = "openshift-kube-descheduler-operator"

func newGateRig(t *testing.T) *gateRig {
	g := &gateRig{t: t, grants: map[string][]grant{}}
	g.fc = newFakeCluster(t)
	g.fc.readsVMs = func(token, ns string) bool { return ns == "team-a-dev" || ns == "orphans-ns" }
	g.fc.namespaces = append(g.fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain"}})
	g.fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, gr := range g.grants[token] {
			if gr.covers(a) {
				return true
			}
		}
		return false
	}
	g.fc.on(http.MethodGet, "/api/v1/nodes/w1", func(w http.ResponseWriter, r *http.Request) { writeObj(w, node("w1", false, false, true)) })
	g.d = &gatedDraft{planeDraft: &planeDraft{declared: map[model.ObjectRef]string{}}}
	for _, ref := range []model.ObjectRef{
		{Kind: model.KindUDN.Kind, Namespace: "team-a-dev", Name: "n1"},
		{Kind: model.KindNetpol.Kind, Namespace: "team-a-dev", Name: "p1"},
		{Kind: model.KindEgressFW.Kind, Namespace: "team-a-dev", Name: "default"},
		{Kind: model.KindCUDN.Kind, Name: "c1"},
		{Kind: model.KindNNCP.Kind, Name: "uplink-x"},
		{Kind: model.KindEgressIP.Kind, Name: "e1"},
		{Kind: model.KindExtRoute.Kind, Name: "r1"},
		{Kind: model.KindANP.Kind, Name: "a1"},
		{Kind: model.KindBANP.Kind, Name: "default"},
	} {
		g.d.declared[ref] = ref.Name + ".yaml"
	}
	g.s = g.fc.server(g.d, Config{PlatformRepo: platformRepo, UploadProxyURL: "https://upload.example"})
	o, err := auth.NewOAuth(auth.OAuthConfig{ClientID: "dotvirt", ClientSecret: "s", RedirectURL: "https://dotvirt.example/api/auth/callback"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.s.oauth = o
	return g
}

// token mints a token holding exactly gs.
func (g *gateRig) token(gs []grant) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	tok := fmt.Sprintf("tok-%d", g.n)
	g.grants[tok] = gs
	return tok
}

// caps reads both capability scopes as tok, the namespace one for ns.
func (g *gateRig) caps(tok, ns string) (model.ClusterCaps, model.NamespaceCaps) {
	g.t.Helper()
	rec := do(g.s, tok, http.MethodGet, "/api/capabilities", "")
	wantStatus(g.t, rec, http.StatusOK)
	cl := decodeBody[model.Capabilities](g.t, rec).Cluster
	rec = do(g.s, tok, http.MethodGet, "/api/capabilities?namespace="+ns, "")
	wantStatus(g.t, rec, http.StatusOK)
	nc := decodeBody[model.Capabilities](g.t, rec).Namespace
	if cl == nil || nc == nil {
		g.t.Fatalf("capabilities missing a scope: %+v %+v", cl, nc)
	}
	return *cl, *nc
}

// gatedDraft is planeDraft plus the stagers the VM and object routes reach,
// recording what an adoption captured.
type gatedDraft struct {
	*planeDraft
	adopted []model.Adoptable
}

func (d *gatedDraft) StageCreateVM(auth.Identity, project.ProjectInfo, json.RawMessage) (model.DraftView, error) {
	return stagedView, nil
}
func (d *gatedDraft) Adopt(auth.Identity, project.ProjectInfo, string, string) (model.DraftView, error) {
	return stagedView, nil
}
func (d *gatedDraft) StageDelete(auth.Identity, project.ProjectInfo, string, string, string) (model.DraftView, error) {
	return stagedView, nil
}
func (d *gatedDraft) StageUpdateManifest(auth.Identity, project.ProjectInfo, string, string, string, model.UpdateManifestRequest) (model.DraftView, error) {
	return stagedView, nil
}
func (d *gatedDraft) RestoreVersion(auth.Identity, project.ProjectInfo, string, string, string, string) (model.DraftView, error) {
	return stagedView, nil
}
func (d *gatedDraft) AdoptObjects(_ auth.Identity, _ project.ProjectInfo, _ string, objs []model.Adoptable) (model.DraftView, error) {
	d.adopted = objs
	return stagedView, nil
}
func (d *gatedDraft) Resync(ctx context.Context, may func(context.Context, string, string) (bool, error), _ string, ns, name string) (model.ResyncResult, error) {
	ok, err := may(ctx, ns, name)
	if err != nil {
		return model.ResyncResult{}, err
	}
	if !ok {
		return model.ResyncResult{}, fmt.Errorf("%w: resync", model.ErrForbidden)
	}
	return model.ResyncResult{Application: "app"}, nil
}

func kind(verb string, k model.Kind, ns string) grant {
	if k.ClusterScoped {
		ns = ""
	}
	return grant{verb: verb, group: k.Group, resource: k.Plural, ns: ns}
}

// gateCase is one gated action: the route that performs it and the rights
// its own gate asks (routeNeeds), every right the capability field offering
// it asks (needs: the route's, and what the coordinator's gate on the staged
// change or the apiserver on an imperative call asks on top), and that field.
type gateCase struct {
	name       string
	ns         string // the namespace the capability is read for; team-a-dev when empty
	call       func(s *Server, tok string) *httptest.ResponseRecorder
	routeNeeds []grant
	needs      []grant
	cap        func(model.ClusterCaps, model.NamespaceCaps) bool
}

func route(method, path, body string) func(*Server, string) *httptest.ResponseRecorder {
	return func(s *Server, tok string) *httptest.ResponseRecorder { return do(s, tok, method, path, body) }
}

func objectCases(label string, k model.Kind, ns, objPath, create, body string) []gateCase {
	field := func(c model.ClusterCaps, n model.NamespaceCaps) model.ObjectCaps {
		switch k {
		case model.KindUDN:
			return n.Network
		case model.KindNetpol:
			return n.NetworkPolicy
		case model.KindEgressFW:
			return n.EgressFirewall
		case model.KindCUDN:
			return c.Network
		case model.KindNNCP:
			return c.Uplink
		case model.KindEgressIP:
			return c.EgressIP
		case model.KindExtRoute:
			return c.ExternalRoute
		case model.KindANP:
			return c.AdminNetworkPolicy
		case model.KindBANP:
			return c.BaselineAdminNetworkPolicy
		}
		panic(k.Kind)
	}
	c := func(c model.ClusterCaps, n model.NamespaceCaps) bool { return field(c, n).Create }
	u := func(c model.ClusterCaps, n model.NamespaceCaps) bool { return field(c, n).Update }
	d := func(c model.ClusterCaps, n model.NamespaceCaps) bool { return field(c, n).Delete }
	one := func(verb string) []grant { return []grant{kind(verb, k, ns)} }
	return []gateCase{
		{name: label + " create", call: route(http.MethodPost, create, body), routeNeeds: one(verbCreate), needs: one(verbCreate), cap: c},
		// The create form re-submitted for a declared object edits it.
		{name: label + " form edit", call: route(http.MethodPost, create, body), routeNeeds: one(verbUpdate), needs: one(verbUpdate), cap: u},
		{name: label + " manifest edit", call: route(http.MethodPut, "/api/objects/"+objPath, `{"yaml":"x"}`), routeNeeds: one(verbUpdate), needs: one(verbUpdate), cap: u},
		{name: label + " restore", call: route(http.MethodPost, "/api/objects/"+objPath+"/restore", `{"hash":"`+hash40+`"}`), routeNeeds: one(verbUpdate), needs: one(verbUpdate), cap: u},
		{name: label + " delete", call: route(http.MethodDelete, "/api/objects/"+objPath, ""), routeNeeds: one(verbDelete), needs: one(verbDelete), cap: d},
	}
}

func gateCases() []gateCase {
	vm := func(verb string) []grant { return []grant{kind(verb, model.KindVM, "team-a-dev")} }
	ns := func(verb, name string) grant { return grant{verb: verb, resource: "namespaces", name: name} }
	rbIn := func(ns string) grant {
		return grant{verb: verbCreate, group: "rbac.authorization.k8s.io", resource: "rolebindings", ns: ns}
	}
	bindIn := func(ns string) grant {
		return grant{verb: verbBind, group: "rbac.authorization.k8s.io", resource: "clusterroles", ns: ns, name: "admin"}
	}
	scheduler := func(verb string) grant {
		return grant{verb: verb, group: "operator.openshift.io", resource: "kubedeschedulers", ns: descheduler}
	}
	drs := []grant{
		scheduler(verbCreate),
		ns(verbCreate, ""),
		{verb: verbCreate, group: "operators.coreos.com", resource: "operatorgroups", ns: descheduler},
		{verb: verbCreate, group: "operators.coreos.com", resource: "subscriptions", ns: descheduler},
	}
	nsCap := func(f func(model.NamespaceCaps) bool) func(model.ClusterCaps, model.NamespaceCaps) bool {
		return func(_ model.ClusterCaps, n model.NamespaceCaps) bool { return f(n) }
	}
	clCap := func(f func(model.ClusterCaps) bool) func(model.ClusterCaps, model.NamespaceCaps) bool {
		return func(c model.ClusterCaps, _ model.NamespaceCaps) bool { return f(c) }
	}
	// The imperative operations have no gate of their own: the apiserver asks
	// this question of the call itself, under the caller's token.
	imperative := func(name, verb, group, resource, sub string, f func(model.NamespaceCaps) bool) gateCase {
		return gateCase{name: name, needs: []grant{{verb: verb, group: group, resource: resource, sub: sub, ns: "team-a-dev"}}, cap: nsCap(f)}
	}
	const kvSub = "subresources.kubevirt.io"
	cases := []gateCase{
		{name: "vm create", call: route(http.MethodPost, "/api/vms", `{"namespace":"team-a-dev","name":"x"}`),
			routeNeeds: vm(verbCreate), needs: vm(verbCreate), cap: nsCap(func(n model.NamespaceCaps) bool { return n.VM.Create })},
		{name: "template deploy", call: route(http.MethodPost, "/api/templates/deploy", `{"namespace":"team-a-dev","template":"t"}`),
			routeNeeds: vm(verbCreate), needs: vm(verbCreate), cap: nsCap(func(n model.NamespaceCaps) bool { return n.VM.Create })},
		{name: "vm adopt", call: route(http.MethodPost, vmPath+"/adopt", ""),
			routeNeeds: vm(verbUpdate), needs: vm(verbUpdate), cap: nsCap(func(n model.NamespaceCaps) bool { return n.VM.Update })},
		{name: "vm delete", call: route(http.MethodPost, vmPath+"/delete", ""),
			routeNeeds: vm(verbDelete), needs: vm(verbDelete), cap: nsCap(func(n model.NamespaceCaps) bool { return n.VM.Delete })},
		{name: "resync", call: route(http.MethodPost, vmPath+"/resync", ""),
			routeNeeds: vm(verbUpdate), needs: vm(verbUpdate), cap: nsCap(func(n model.NamespaceCaps) bool { return n.Resync })},
		imperative("restart", verbUpdate, kvSub, "virtualmachines", "restart", func(n model.NamespaceCaps) bool { return n.Restart }),
		imperative("migrate", verbUpdate, kvSub, "virtualmachines", "migrate", func(n model.NamespaceCaps) bool { return n.Migrate }),
		imperative("pause", verbUpdate, kvSub, "virtualmachineinstances", "pause", func(n model.NamespaceCaps) bool { return n.Pause }),
		imperative("unpause", verbUpdate, kvSub, "virtualmachineinstances", "unpause", func(n model.NamespaceCaps) bool { return n.Unpause }),
		imperative("console", verbGet, kvSub, "virtualmachineinstances", "vnc", func(n model.NamespaceCaps) bool { return n.Console }),
		imperative("snapshot take", verbCreate, "snapshot.kubevirt.io", "virtualmachinesnapshots", "", func(n model.NamespaceCaps) bool { return n.SnapshotTake }),
		imperative("snapshot restore", verbCreate, "snapshot.kubevirt.io", "virtualmachinerestores", "", func(n model.NamespaceCaps) bool { return n.SnapshotRestore }),
		imperative("snapshot delete", verbDelete, "snapshot.kubevirt.io", "virtualmachinesnapshots", "", func(n model.NamespaceCaps) bool { return n.SnapshotDelete }),
		imperative("clone", verbCreate, "clone.kubevirt.io", "virtualmachineclones", "", func(n model.NamespaceCaps) bool { return n.Clone }),
		imperative("upload", verbCreate, "cdi.kubevirt.io", "datavolumes", "", func(n model.NamespaceCaps) bool { return n.DataVolumeCreate }),
		{name: "project template edit", call: route(http.MethodPut, "/api/templates", `{"library":"team-a","name":"t","yaml":"x"}`),
			routeNeeds: vm(verbUpdate), needs: vm(verbUpdate), cap: nsCap(func(n model.NamespaceCaps) bool { return n.Project.TemplateEdit })},
		// Attaching a repo claims each existing namespace at the route, and
		// writes Namespace files the platform repo does not hold yet.
		{name: "attach repo", ns: "orphans-ns", call: route(http.MethodPost, "/api/projects/orphans/adopt", ""),
			routeNeeds: []grant{ns(verbUpdate, "orphans-ns")}, needs: []grant{ns(verbUpdate, "orphans-ns"), ns(verbCreate, "")},
			cap: nsCap(func(n model.NamespaceCaps) bool { return n.Project.Adopt })},
		{name: "attach repo with owners", ns: "orphans-ns", call: route(http.MethodPost, "/api/projects/orphans/adopt", `{"owners":["alice"]}`),
			routeNeeds: []grant{ns(verbUpdate, "orphans-ns")},
			needs:      []grant{ns(verbUpdate, "orphans-ns"), ns(verbCreate, ""), rbIn("orphans-ns"), bindIn("orphans-ns")},
			cap:        nsCap(func(n model.NamespaceCaps) bool { return n.Project.Adopt && n.Project.GrantAdmin })},
		{name: "release", ns: "orphans-ns", call: route(http.MethodPost, "/api/projects/orphans/release", ""),
			needs: []grant{ns(verbUpdate, "orphans-ns")}, cap: nsCap(func(n model.NamespaceCaps) bool { return n.Project.Release })},
		{name: "project over an existing namespace", call: route(http.MethodPost, "/api/projects", `{"name":"team-c","namespace":"plain"}`),
			routeNeeds: []grant{ns(verbUpdate, "plain")}},
		{name: "new namespace", call: route(http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"newns"}`),
			needs: []grant{ns(verbCreate, "")}, cap: clCap(func(c model.ClusterCaps) bool { return c.NamespaceCreate })},
		{name: "new namespace with a VM Network", call: route(http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"newns","vmNetwork":{"name":"v","subnet":"10.0.0.0/24"}}`),
			needs: []grant{ns(verbCreate, ""), kind(verbCreate, model.KindUDN, "")}, cap: clCap(func(c model.ClusterCaps) bool { return c.VMNetwork })},
		{name: "new project with owners", call: route(http.MethodPost, "/api/projects", `{"name":"team-c","owners":["alice"]}`),
			needs: []grant{ns(verbCreate, ""), rbIn(""), bindIn("")}, cap: clCap(func(c model.ClusterCaps) bool { return c.GrantAdmin })},
		{name: "shared library", call: route(http.MethodPut, "/api/templates", `{"library":"platform","name":"t","yaml":"x"}`),
			routeNeeds: []grant{kind(verbCreate, model.KindTemplate, "")}, needs: []grant{kind(verbCreate, model.KindTemplate, "")},
			cap: clCap(func(c model.ClusterCaps) bool { return c.SharedLibrary })},
		{name: "drs enable", call: route(http.MethodPost, "/api/drs", `{}`),
			routeNeeds: []grant{scheduler(verbCreate)}, needs: drs, cap: clCap(func(c model.ClusterCaps) bool { return c.DRSEnable })},
		{name: "drs enable with PSI", call: route(http.MethodPost, "/api/drs", `{"installPSI":true}`),
			routeNeeds: []grant{scheduler(verbCreate)}, needs: append(append([]grant(nil), drs...), kind(verbCreate, model.KindMachCfg, "")),
			cap: clCap(func(c model.ClusterCaps) bool { return c.DRSPSI })},
		{name: "drs disable", call: route(http.MethodDelete, "/api/drs", ""),
			routeNeeds: []grant{scheduler(verbDelete)}, needs: []grant{scheduler(verbDelete)}, cap: clCap(func(c model.ClusterCaps) bool { return c.DRSDisable })},
		// The apply runs under the caller's token; the apiserver asks these.
		{name: "finish SSO", needs: []grant{{verb: verbCreate, group: "oauth.openshift.io", resource: "oauthclients"}, {verb: verbPatch, group: "oauth.openshift.io", resource: "oauthclients", name: "dotvirt"}},
			cap: clCap(func(c model.ClusterCaps) bool { return c.OAuthClient })},
	}
	cases = append(cases, objectCases("project network", model.KindUDN, "team-a-dev", "network/team-a-dev/n1", "/api/networks", `{"scope":"project","namespace":"team-a-dev"}`)...)
	cases = append(cases, objectCases("network policy", model.KindNetpol, "team-a-dev", "networkpolicy/team-a-dev/p1", "/api/networkpolicies", `{"namespace":"team-a-dev"}`)...)
	cases = append(cases, objectCases("egress firewall", model.KindEgressFW, "team-a-dev", "egressfirewall/team-a-dev/default", "/api/egressfirewalls", `{"namespace":"team-a-dev"}`)...)
	cases = append(cases, objectCases("shared network", model.KindCUDN, "", "network/cluster/c1", "/api/networks", `{"scope":"shared"}`)...)
	cases = append(cases, objectCases("uplink", model.KindNNCP, "", "uplink/cluster/uplink-x", "/api/uplinks", `{}`)...)
	cases = append(cases, objectCases("egress IP", model.KindEgressIP, "", "egressip/cluster/e1", "/api/egressips", `{}`)...)
	cases = append(cases, objectCases("external route", model.KindExtRoute, "", "externalroute/cluster/r1", "/api/externalroutes", `{}`)...)
	cases = append(cases, objectCases("admin policy", model.KindANP, "", "adminnetworkpolicy/cluster/a1", "/api/adminnetworkpolicies", `{"name":"a1","priority":10}`)...)
	cases = append(cases, objectCases("baseline policy", model.KindBANP, "", "baselineadminnetworkpolicy/cluster/default", "/api/adminnetworkpolicies", `{"baseline":true}`)...)
	return cases
}

func without(gs []grant, i int) []grant {
	return append(append([]grant(nil), gs[:i]...), gs[i+1:]...)
}

// Every route gate refuses without each right it asks and allows with them,
// and every capability field reads true exactly when it holds every right the
// route, the coordinator or the apiserver asks of its action.
func TestGatesAreExactAndCapabilitiesMatch(t *testing.T) {
	g := newGateRig(t)
	g.s.drift = argo.NewSnapshot(nil, nil)
	for _, c := range gateCases() {
		t.Run(c.name, func(t *testing.T) {
			g.t = t
			ns := c.ns
			if ns == "" {
				ns = "team-a-dev"
			}
			if c.call != nil {
				all := append(append([]grant(nil), c.needs...), c.routeNeeds...)
				if rec := c.call(g.s, g.token(all)); rec.Code == http.StatusForbidden {
					t.Errorf("with every right: route = %d %q", rec.Code, rec.Body.String())
				}
				for i, q := range c.routeNeeds {
					rest := without(c.routeNeeds, i)
					for _, n := range c.needs {
						if n != q {
							rest = append(rest, n)
						}
					}
					if rec := c.call(g.s, g.token(rest)); rec.Code != http.StatusForbidden {
						t.Errorf("without %v: route = %d %q, want 403", q, rec.Code, rec.Body.String())
					}
				}
			}
			if c.cap == nil {
				return
			}
			if cl, nc := g.caps(g.token(c.needs), ns); !c.cap(cl, nc) {
				t.Errorf("with %v: capability false", c.needs)
			}
			for i := range c.needs {
				if cl, nc := g.caps(g.token(without(c.needs, i)), ns); c.cap(cl, nc) {
					t.Errorf("without %v: capability true", c.needs[i])
				}
			}
		})
	}
}

// The lane is any platform authoring signal: each one opens it, none refuses,
// and the capability says the same.
func TestPlatformLaneMatchesItsGate(t *testing.T) {
	g := newGateRig(t)
	for _, gs := range [][]grant{
		{kind(verbCreate, model.KindCUDN, "")},
		{kind(verbCreate, model.KindDesched, "")},
		{kind(verbCreate, model.KindTemplate, "")},
		nil,
	} {
		tok := g.token(gs)
		cl, _ := g.caps(tok, "team-a-dev")
		rec := do(g.s, tok, http.MethodGet, "/api/draft?project=platform", "")
		if allowed := rec.Code != http.StatusForbidden; allowed != cl.Platform || allowed != (gs != nil) {
			t.Errorf("with %v: draft = %d, capability = %v", gs, rec.Code, cl.Platform)
		}
	}
}

// The evacuation gates on patching the node - the question the apiserver
// asks of cordon and maintenance - and NodeInfo carries that verdict.
func TestNodeGateMatchesCanCordon(t *testing.T) {
	g := newGateRig(t)
	for _, c := range []struct {
		gs   []grant
		want bool
	}{
		{[]grant{{verb: verbPatch, resource: "nodes", name: "w1"}}, true},
		{[]grant{{verb: verbPatch, resource: "nodes"}}, true},
		{[]grant{{verb: verbUpdate, resource: "nodes"}}, false},
	} {
		tok := g.token(c.gs)
		rec := do(g.s, tok, http.MethodGet, "/api/nodes/w1", "")
		wantStatus(t, rec, http.StatusOK)
		if can := decodeBody[model.NodeInfo](t, rec).CanCordon; can != c.want {
			t.Errorf("with %v: CanCordon = %v, want %v", c.gs, can, c.want)
		}
		rec = do(g.s, tok, http.MethodPost, "/api/nodes/w1/evacuate", "")
		if allowed := rec.Code != http.StatusForbidden; allowed != c.want {
			t.Errorf("with %v: evacuate = %d %q, want allowed=%v", c.gs, rec.Code, rec.Body.String(), c.want)
		}
	}
}

// Joining a namespace another project holds would take it from that project.
func TestProjectRefusesANamespaceOfAnotherProject(t *testing.T) {
	g := newGateRig(t)
	tok := g.token([]grant{{verb: verbUpdate, resource: "namespaces"}, {verb: verbCreate, resource: "namespaces"}})
	for _, body := range []string{`{"name":"team-c","namespace":"orphans-ns"}`} {
		rec := do(g.s, tok, http.MethodPost, "/api/projects", body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "orphans") {
			t.Errorf("project over orphans-ns = %d %q, want 409 naming its project", rec.Code, rec.Body.String())
		}
	}
	rec := do(g.s, tok, http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"orphans-ns"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("namespace join of orphans-ns = %d %q, want 409", rec.Code, rec.Body.String())
	}
	g.d.none(t)
}

// A namespace adoption leaves out the kinds the caller may not create, as the
// coordinator would refuse them, and says so; with none left it refuses.
func TestNamespaceAdoptFiltersByCreateRight(t *testing.T) {
	g := newGateRig(t)
	g.fc.on(http.MethodGet, "/apis/kubevirt.io/v1/namespaces/team-a-dev/virtualmachines", unstructuredList("kubevirt.io/v1", "VirtualMachine",
		map[string]any{"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine", "metadata": map[string]any{"name": "web", "namespace": "team-a-dev"}}))
	g.fc.on(http.MethodGet, "/apis/networking.k8s.io/v1/namespaces/team-a-dev/networkpolicies", unstructuredList("networking.k8s.io/v1", "NetworkPolicy",
		map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "p1", "namespace": "team-a-dev"}}))

	tok := g.token([]grant{kind(verbUpdate, model.KindVM, "team-a-dev"), kind(verbCreate, model.KindVM, "team-a-dev")})
	rec := do(g.s, tok, http.MethodPost, "/api/namespaces/team-a-dev/adopt", "")
	wantStatus(t, rec, http.StatusOK)
	if len(g.d.adopted) != 1 || g.d.adopted[0].Kind != "VirtualMachine" {
		t.Errorf("adopted %+v, want the VM alone", g.d.adopted)
	}
	if w := decodeBody[model.DraftView](t, rec).Warning; !strings.Contains(w, "may not create networkpolicies") {
		t.Errorf("warning = %q, want the refused kind named", w)
	}

	// The cluster scope filters the same way.
	g.fc.on(http.MethodGet, "/apis/k8s.ovn.org/v1/egressips", unstructuredList("k8s.ovn.org/v1", "EgressIP",
		map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP", "metadata": map[string]any{"name": "e9"}}))
	lane := g.token([]grant{kind(verbCreate, model.KindCUDN, "")})
	rec = do(g.s, lane, http.MethodPost, "/api/platform/adopt", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "egressips") {
		t.Errorf("platform adopt without create = %d %q, want 403 naming egressips", rec.Code, rec.Body.String())
	}
}

// An evacuation names a failed VM only where the caller may read VMs, and
// counts the rest.
func TestEvacuateHidesUnreadableFailures(t *testing.T) {
	g := newGateRig(t)
	g.fc.vmis = nil
	vmi := func(ns, name string) kubevirtcorev1.VirtualMachineInstance {
		return kubevirtcorev1.VirtualMachineInstance{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Status: kubevirtcorev1.VirtualMachineInstanceStatus{Phase: kubevirtcorev1.Running, NodeName: "w1"}}
	}
	g.fc.vmis = []kubevirtcorev1.VirtualMachineInstance{vmi("team-a-dev", "web"), vmi("secret-ns", "hidden")}
	s := g.fc.server(g.d, Config{})
	tok := g.token([]grant{{verb: verbPatch, resource: "nodes", name: "w1"}})
	rec := do(s, tok, http.MethodPost, "/api/nodes/w1/evacuate", "")
	wantStatus(t, rec, http.StatusOK)
	ev := decodeBody[model.Evacuation](t, rec)
	if len(ev.Failures) != 1 || ev.Failures[0].Name != "web" || ev.Hidden != 1 || strings.Contains(rec.Body.String(), "secret-ns") {
		t.Errorf("evacuation = %+v, want web listed and one hidden", ev)
	}
}
