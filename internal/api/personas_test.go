package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// The supported personas (docs/personas.md), answered by the rules a real
// apiserver would weigh: view, edit and admin as aggregated on each platform
// (testdata/personas, captured from a cluster), the roles dotvirt's operator
// ships (operand_roles.yaml), and what each platform grants every signed-in
// user. The MicroShift lane proves the captured rules against a real
// apiserver.

// personaRole is one ClusterRole's rules.
type personaRole struct {
	Name  string              `json:"name"`
	Rules []rbacv1.PolicyRule `json:"rules"`
}

// personaGrant is a role bound in namespaces, or cluster-wide when there are none.
type personaGrant struct {
	rules      []rbacv1.PolicyRule
	namespaces []string
}

// allows reports whether any of gs allows a, as RBAC answers it: a
// RoleBinding's rules reach only its namespace, never a cluster-scoped or
// all-namespaces request.
func allows(gs []personaGrant, a *authzv1.ResourceAttributes) bool {
	resource := a.Resource
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	has := func(set []string, v string) bool { return slices.Contains(set, "*") || slices.Contains(set, v) }
	for _, g := range gs {
		if len(g.namespaces) > 0 && (a.Namespace == "" || !slices.Contains(g.namespaces, a.Namespace)) {
			continue
		}
		for _, r := range g.rules {
			if has(r.Verbs, a.Verb) && has(r.APIGroups, a.Group) && has(r.Resources, resource) &&
				(len(r.ResourceNames) == 0 || slices.Contains(r.ResourceNames, a.Name)) {
				return true
			}
		}
	}
	return false
}

// personaRoles loads platform's captured roles and the operator's.
func personaRoles(t *testing.T, platform string) map[string][]rbacv1.PolicyRule {
	t.Helper()
	roles := map[string][]rbacv1.PolicyRule{"cluster-admin": {{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}}}}
	files, err := filepath.Glob(filepath.Join("testdata", "personas", platform, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no captured roles for %s: %v", platform, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var r personaRole
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		roles[r.Name] = r.Rules
	}
	f, err := os.Open(filepath.Join("..", "..", "operator", "config", "rbac", "operand_roles.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var r rbacv1.ClusterRole
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if r.Name != "" {
			roles[r.Name] = r.Rules
		}
	}
	return roles
}

// signedIn are the roles each platform binds to system:authenticated that
// dotvirt's reads touch. OpenShift: basic-user (storage classes, and snapshot
// classes through the CSI snapshot controller's aggregate). MicroShift: the
// operator's dotvirt-storage-catalog (no clusterversions). Both: KubeVirt's
// and CDI's catalog readers.
var signedIn = map[string][]string{
	"openshift":  {"basic-user", "instancetype.kubevirt.io:view", "cdi.kubevirt.io:config-reader"},
	"microshift": {"system:basic-user", "instancetype.kubevirt.io:view", "cdi.kubevirt.io:config-reader", "dotvirt-storage-catalog"},
}

const (
	pViewerToken       = "persona-viewer"
	pAuthorToken       = "persona-author"
	pProjectAdminToken = "persona-project-admin"
	pNetworkAdminToken = "persona-network-admin"
	pClusterAdminToken = "persona-cluster-admin"
	// Not a persona: signed in and bound to nothing, the baseline.
	pSignedInToken = "persona-signed-in"
)

// personaGrants is what each persona holds besides signedIn, as
// docs/personas.md binds it: the project personas in team-a-dev.
func personaGrants() map[string]map[string][]string {
	inTeamA := func(roles ...string) map[string][]string { return map[string][]string{"team-a-dev": roles} }
	return map[string]map[string][]string{
		pViewerToken:       inTeamA("view", tenancy.TenantEgressViewRole),
		pAuthorToken:       inTeamA("edit", tenancy.TenantEgressViewRole),
		pProjectAdminToken: inTeamA("admin", tenancy.TenantEgressRole),
		pNetworkAdminToken: {"": {"dotvirt-platform-network-admin"}},
		pClusterAdminToken: {"": {"cluster-admin"}},
		pSignedInToken:     {},
	}
}

// personaServer is the fake apiserver weighing each persona's rules, serving
// the kinds platform serves, behind a tenant repo declaring every tenant kind.
func personaServer(t *testing.T, platform string) *Server {
	t.Helper()
	roles := personaRoles(t, platform)
	byToken := map[string][]personaGrant{}
	for token, bound := range personaGrants() {
		var gs []personaGrant
		for _, name := range signedIn[platform] {
			gs = append(gs, personaGrant{rules: roles[name]})
		}
		for ns, names := range bound {
			for _, name := range names {
				rules, ok := roles[name]
				if !ok {
					t.Fatalf("%s: no role %s on %s", token, name, platform)
				}
				g := personaGrant{rules: rules}
				if ns != "" {
					g.namespaces = []string{ns}
				}
				gs = append(gs, g)
			}
		}
		byToken[token] = gs
	}
	fc := newFakeCluster(t)
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool { return allows(byToken[token], a) }
	served := []model.Kind{model.KindNetpol, model.KindDV, model.KindNAD}
	if platform == "openshift" {
		served = append(served, model.KindUDN, model.KindEgressFW)
	}
	fc.serveKinds(served...)
	fc.on(http.MethodGet, "/apis/storage.k8s.io/v1/storageclasses", unstructuredList("storage.k8s.io/v1", "StorageClass",
		map[string]any{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": map[string]any{"name": "fast"}, "provisioner": "csi.example"}))
	var declares []schema.GroupKind
	for _, k := range tenancy.TenantKinds {
		declares = append(declares, schema.GroupKind{Group: k.Group, Kind: k.Kind})
	}
	return fc.server(gitReads{&planeDraft{kinds: declares}}, Config{PlatformRepo: platformRepo, BaseBranch: "main"})
}

// fingerprint is what one persona is shown and offered.
type fingerprint struct {
	Projects       string // the projects the caller sees
	RepoRead       bool   // team-a's whole repo
	VMCreate       bool
	Console        bool
	EgressFWCreate bool
	NetworkCreate  bool // a project network (UDN)
	StorageClasses bool // the wizard's storage classes
	Hosts          bool
	PlatformRead   bool
	Platform       bool // may stage a platform change
}

func personaFingerprint(t *testing.T, s *Server, token string) fingerprint {
	t.Helper()
	var fp fingerprint
	id, c, err := s.userCluster(asUser(httptest.NewRequest(http.MethodGet, "/", nil), token))
	if err != nil {
		t.Fatal(err)
	}
	projs, err := s.projectsFor(t.Context(), id, c)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range projs {
		names = append(names, p.Name)
	}
	fp.Projects = strings.Join(names, ",")
	if rec := do(s, token, http.MethodGet, "/api/capabilities?namespace=team-a-dev", ""); rec.Code != http.StatusOK {
		wantStatus(t, rec, http.StatusNotFound)
	} else if c := decodeBody[model.Capabilities](t, rec).Namespace; c != nil {
		fp.RepoRead, fp.VMCreate, fp.Console = c.Project.RepoRead, c.VM.Create, c.Console
		fp.EgressFWCreate, fp.NetworkCreate = c.EgressFirewall.Create, c.Network.Create
	}
	if c := decodeBody[model.Capabilities](t, do(s, token, http.MethodGet, "/api/capabilities", "")).Cluster; c != nil {
		fp.Hosts, fp.PlatformRead, fp.Platform = c.Hosts, c.PlatformRead, c.Platform
	}
	fp.StorageClasses = len(decodeBody[model.Options](t, do(s, token, http.MethodGet, "/api/options", "")).StorageClasses) > 0
	return fp
}

// Each persona gets exactly its answers on each platform. A change here is a
// change to what docs/personas.md promises.
func TestPersonas(t *testing.T) {
	project := fingerprint{Projects: "team-a", RepoRead: true, StorageClasses: true}
	author := project
	author.VMCreate, author.Console, author.NetworkCreate = true, true, true
	admin := author
	admin.EgressFWCreate = true
	all := fingerprint{Projects: "orphans,team-a", RepoRead: true, VMCreate: true, Console: true, EgressFWCreate: true,
		NetworkCreate: true, StorageClasses: true, Hosts: true, PlatformRead: true, Platform: true}
	// Stages platform changes; reading the platform repo would need list on
	// every kind it declares, which no shipped role grants.
	network := fingerprint{StorageClasses: true, Platform: true}
	signedIn := fingerprint{StorageClasses: true}
	want := map[string]map[string]fingerprint{
		"openshift": {pViewerToken: project, pAuthorToken: author, pProjectAdminToken: admin,
			pNetworkAdminToken: network, pClusterAdminToken: all, pSignedInToken: signedIn},
		"microshift": {pViewerToken: project, pAuthorToken: author, pProjectAdminToken: admin,
			pNetworkAdminToken: network, pClusterAdminToken: all, pSignedInToken: signedIn},
	}
	// MicroShift serves no UserDefinedNetworks and its stock roles grant none.
	for _, token := range []string{pAuthorToken, pProjectAdminToken} {
		fp := want["microshift"][token]
		fp.NetworkCreate = false
		want["microshift"][token] = fp
	}
	for platform, personas := range want {
		s := personaServer(t, platform)
		for token, w := range personas {
			if got := personaFingerprint(t, s, token); got != w {
				t.Errorf("%s %s:\n got %+v\nwant %+v", platform, token, got, w)
			}
		}
	}
}
