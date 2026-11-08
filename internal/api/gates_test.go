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
	"github.com/epheo/dotvirt/internal/changeset"
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

// newGrantCluster is the fake apiserver of a rig: reviews and dry runs answer
// from the token's grants, and every token sees team-a-dev and orphans-ns.
func newGrantCluster(t *testing.T) *gateRig {
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
	g.fc.dryRuns = g.fc.ssar
	return g
}

func newGateRig(t *testing.T) *gateRig {
	g := newGrantCluster(t)
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
	g.fc.vms = []kubevirtcorev1.VirtualMachine{{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a-dev", Name: "web"}}}
	g.s = g.fc.server(g.d, Config{PlatformRepo: platformRepo, UploadProxyURL: "https://upload.example"})
	g.d.authz = g.s
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
	authz   changeset.Authorizer
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

// Resync asks the server, as the coordinator does, its ResyncNeeds.
func (d *gatedDraft) Resync(_ context.Context, id auth.Identity, proj project.ProjectInfo, ns, name string) (model.ResyncResult, error) {
	need, _ := d.ResyncNeeds(proj, ns, name)
	if err := d.authz.Authorize(id, need); err != nil {
		return model.ResyncResult{}, err
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
		// An upload creates its DataVolume, then its token request: gated whole
		// before either is made.
		{name: "upload", call: route(http.MethodPost, "/api/uploads", `{"namespace":"team-a-dev","name":"img","size":"1Gi"}`),
			routeNeeds: bootImageGrants[:2], needs: bootImageGrants[:2],
			cap: nsCap(func(n model.NamespaceCaps) bool { return n.DataVolumeCreate })},
		// Add a boot image: the upload plus its DataSource, gated whole before
		// anything is made, so a token without datasources create reads the
		// field false and is refused by the route.
		{name: "boot image upload", call: route(http.MethodPost, "/api/uploads", `{"namespace":"team-a-dev","name":"img","size":"1Gi","bootImage":true}`),
			routeNeeds: bootImageGrants, needs: bootImageGrants,
			cap: nsCap(func(n model.NamespaceCaps) bool { return n.BootImageCreate })},
		{name: "project template edit", call: route(http.MethodPut, "/api/templates", `{"library":"team-a","name":"t","yaml":"x"}`),
			routeNeeds: vm(verbUpdate), needs: vm(verbUpdate), cap: nsCap(func(n model.NamespaceCaps) bool { return n.Project.TemplateEdit })},
		{name: "shared library", call: route(http.MethodPut, "/api/templates", `{"library":"platform","name":"t","yaml":"x"}`),
			routeNeeds: []grant{kind(verbCreate, model.KindTemplate, "")}, needs: []grant{kind(verbCreate, model.KindTemplate, "")},
			cap: clCap(func(c model.ClusterCaps) bool { return c.SharedLibrary })},
		// A NetworkAttachmentDefinition git still declares under the network
		// routes: edited and deleted, never created by a form.
		{name: "network attachment manifest edit", call: route(http.MethodPut, "/api/objects/network/team-a-dev/n2", `{"yaml":"x"}`),
			routeNeeds: []grant{kind(verbUpdate, model.KindNAD, "team-a-dev")}, needs: []grant{kind(verbUpdate, model.KindNAD, "team-a-dev")},
			cap: nsCap(func(n model.NamespaceCaps) bool { return n.NetworkAttachment.Update })},
		{name: "network attachment delete", call: route(http.MethodDelete, "/api/objects/network/team-a-dev/n2", ""),
			routeNeeds: []grant{kind(verbDelete, model.KindNAD, "team-a-dev")}, needs: []grant{kind(verbDelete, model.KindNAD, "team-a-dev")},
			cap: nsCap(func(n model.NamespaceCaps) bool { return n.NetworkAttachment.Delete })},
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

var bootImageGrants = []grant{
	{verb: verbCreate, group: "cdi.kubevirt.io", resource: "datavolumes", ns: "team-a-dev"},
	{verb: verbCreate, group: "upload.cdi.kubevirt.io", resource: "uploadtokenrequests", ns: "team-a-dev"},
	{verb: verbCreate, group: "cdi.kubevirt.io", resource: "datasources", ns: "team-a-dev"},
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

// The platform tier's shared reads (its history, a commit's diff, a proposal,
// the platform draft without one of one's own) open on a cluster-wide signal
// alone, and cluster.platformRead says the same. A right to stage one
// platform kind, or a DRS right inside the operator namespace (which OLM
// folds into that namespace's admin role), stages but reads nothing.
func TestPlatformReadsNeedAClusterWideSignal(t *testing.T) {
	g := newGateRig(t)
	passes := func(tok, path string) (ok bool) {
		defer func() {
			if recover() != nil {
				ok = true // past the gate, into the stubbed reader
			}
		}()
		return do(g.s, tok, http.MethodGet, path, "").Code != http.StatusForbidden
	}
	desched := func(verb, name string) grant {
		return grant{verb: verb, group: "operator.openshift.io", resource: "kubedeschedulers", ns: descheduler, name: name}
	}
	for _, c := range []struct {
		gs            []grant
		read, staging bool
	}{
		{[]grant{{verb: verbList, resource: "namespaces"}}, true, false},
		{[]grant{kind(verbCreate, model.KindCUDN, "")}, true, true},
		{[]grant{{verb: verbCreate, group: "operator.openshift.io", resource: "kubedeschedulers"}}, true, true},
		{[]grant{kind(verbCreate, model.KindTemplate, "")}, true, true},
		{[]grant{kind(verbCreate, model.KindEgressIP, "")}, false, true},
		{[]grant{kind(verbDelete, model.KindNNCP, "")}, false, true},
		{[]grant{desched(verbCreate, ""), desched(verbUpdate, "cluster"), desched(verbDelete, "cluster")}, false, true},
		{nil, false, false},
	} {
		tok := g.token(c.gs)
		cl, _ := g.caps(tok, "team-a-dev")
		if cl.PlatformRead != c.read || cl.Platform != c.staging {
			t.Errorf("with %v: platformRead = %v, platform = %v; want %v, %v", c.gs, cl.PlatformRead, cl.Platform, c.read, c.staging)
		}
		for _, path := range []string{"/api/projects/platform/history", "/api/projects/platform/history/" + hash40,
			"/api/projects/platform/proposals/1", "/api/draft?project=platform"} {
			if got := passes(tok, path); got != c.read {
				t.Errorf("with %v: %s passes = %v, want %v", c.gs, path, got, c.read)
			}
		}
	}
}

// A caller whose only right staged something there - a claim on one
// namespace - still reaches their platform draft: nothing staged is stranded.
func TestOwnPlatformDraftIsReachable(t *testing.T) {
	g := newGateRig(t)
	tok := g.token([]grant{{verb: verbGet, resource: "namespaces", name: "orphans-ns"}, {verb: verbUpdate, resource: "namespaces", name: "orphans-ns"}})
	wantStatus(t, do(g.s, tok, http.MethodGet, "/api/draft?project=platform", ""), http.StatusForbidden)
	wantStatus(t, do(g.s, tok, http.MethodPost, "/api/projects/orphans/adopt", ""), http.StatusOK)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/draft?project=platform", ""},
		{http.MethodDelete, "/api/draft?project=platform", ""},
	} {
		if rec := do(g.s, tok, c.method, c.path, c.body); rec.Code == http.StatusForbidden {
			t.Errorf("%s %s = %d %q, want the own draft reached", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(g.s, tok, http.MethodGet, "/api/projects/platform/history", ""); rec.Code != http.StatusForbidden {
		t.Errorf("platform history = %d, want refused: a draft opens only the draft", rec.Code)
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
// The holding project is named only to a caller who sees it, and only once
// they hold the claim; without the claim the refusal is the claim's.
func TestProjectRefusesANamespaceOfAnotherProject(t *testing.T) {
	g := newGateRig(t)
	claim := []grant{{verb: verbGet, resource: "namespaces"}, {verb: verbUpdate, resource: "namespaces"}, {verb: verbCreate, resource: "namespaces"}}
	tok := g.token(claim)
	rec := do(g.s, tok, http.MethodPost, "/api/projects", `{"name":"team-c","namespace":"orphans-ns"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "project orphans") {
		t.Errorf("project over orphans-ns = %d %q, want 409 naming its project", rec.Code, rec.Body.String())
	}
	rec = do(g.s, tok, http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"orphans-ns"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("namespace join of orphans-ns = %d %q, want 409", rec.Code, rec.Body.String())
	}
	g.d.none(t)

	// A caller who sees only team-a-dev learns no other project's name.
	g.fc.readsVMs = func(token, ns string) bool { return ns == "team-a-dev" }
	rec = do(g.s, g.token(claim), http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"orphans-ns"}`)
	if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "project orphans") {
		t.Errorf("hidden holder = %d %q, want an unnamed 409", rec.Code, rec.Body.String())
	}
	rec = do(g.s, g.token(claim[:1]), http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"orphans-ns"}`)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "project orphans") {
		t.Errorf("no claim = %d %q, want the claim's 403", rec.Code, rec.Body.String())
	}
	g.d.none(t)
}

// Adopt and release resolve their target among the projects the caller sees,
// unless they may relabel any namespace: a hidden project answers as none.
// Whether a project exists is told only past the gate.
func TestTenancyRoutesHideProjects(t *testing.T) {
	g := newGateRig(t)
	g.fc.readsVMs = func(token, ns string) bool { return ns == "team-a-dev" }
	nobody := g.token(nil)
	for _, path := range []string{"/api/projects/orphans/adopt", "/api/projects/orphans/release"} {
		hidden := do(g.s, nobody, http.MethodPost, path, "")
		ghost := do(g.s, nobody, http.MethodPost, strings.Replace(path, "orphans", "ghost", 1), "")
		if hidden.Code != http.StatusNotFound || strings.Replace(hidden.Body.String(), "orphans", "ghost", 1) != ghost.Body.String() {
			t.Errorf("%s: hidden = %d %q, ghost = %d %q, want the same 404", path, hidden.Code, hidden.Body.String(), ghost.Code, ghost.Body.String())
		}
	}
	admin := g.token([]grant{{verb: verbGet, resource: "namespaces"}, {verb: verbUpdate, resource: "namespaces"}})
	if rec := do(g.s, admin, http.MethodPost, "/api/projects/orphans/adopt", ""); rec.Code == http.StatusNotFound {
		t.Errorf("a cluster-wide claim: %d %q, want the project found", rec.Code, rec.Body.String())
	}
	if rec := do(g.s, nobody, http.MethodPost, "/api/projects", `{"name":"orphans"}`); rec.Code == http.StatusConflict {
		t.Errorf("an existing project told to a caller without rights: %q", rec.Body.String())
	}
}

// A namespace adoption leaves out the objects the coordinator would refuse,
// weighed as it weighs them, and names their kinds; with none left it
// refuses.
func TestNamespaceAdoptFiltersByTheGatesWeighing(t *testing.T) {
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
		t.Errorf("platform adopt without the right = %d %q, want 403 naming egressips", rec.Code, rec.Body.String())
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

// A frame asks the platform lane only when the platform tier holds what only
// its authors see: on a quiet platform a caller with no platform right asks
// none of its questions; an open platform proposal brings them back, each
// asked once and then cached.
func TestFrameAsksThePlatformLaneOnlyWhenBusy(t *testing.T) {
	g := newGateRig(t)
	// A caller who sees no project: the frame reads no repo.
	g.fc.readsVMs = func(string, string) bool { return false }
	var mu sync.Mutex
	asked := map[string]int{}
	inner := g.fc.ssar
	g.fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		mu.Lock()
		if a.Resource == "kubedeschedulers" || a.Resource == "virtualmachinetemplates" || a.Verb == verbDelete {
			asked[token]++
		}
		mu.Unlock()
		return inner(token, a)
	}
	frame := func(token string) {
		t.Helper()
		if _, err := g.s.InventoryForIdentity(context.Background(), auth.Identity{Token: token, Username: "u"}); err != nil {
			t.Fatal(err)
		}
	}
	quiet := g.token(nil)
	frame(quiet)
	g.s.proposals.Put(platformProjectName, []model.Proposal{{PRNumber: 1}})
	busy := g.token(nil)
	frame(busy)
	frame(busy)
	mu.Lock()
	defer mu.Unlock()
	if asked[quiet] != 0 || asked[busy] == 0 {
		t.Errorf("lane questions: quiet frame %d, busy frames %d; want none, then some", asked[quiet], asked[busy])
	}
}
