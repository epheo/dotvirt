package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/ttlcache"
)

// questions records the reviews a tenant's token asks, by verb, resource and
// name.
type questions struct {
	mu    sync.Mutex
	asked []string
}

func (q *questions) add(a *authzv1.ResourceAttributes) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.asked = append(q.asked, a.Verb+" "+a.Resource+" "+a.Name)
}

func (q *questions) take() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.asked
	q.asked = nil
	slices.Sort(out)
	return out
}

// A create of an object the SA snapshot holds is asked as the update Argo
// makes of it, unless the change also deletes it: then it is a replace.
func TestAuthorizeAsksARunningObjectsCreateAsItsUpdate(t *testing.T) {
	fc := newFakeCluster(t)
	fc.vms = []kubevirtcorev1.VirtualMachine{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a-dev"}}}
	q := &questions{}
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		if token == tenantToken {
			q.add(a)
		}
		return true
	}
	s := fc.server(nil, Config{})
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	vm := func(verb, name string) changeset.Access {
		return changeset.Access{Verb: verb, Group: "kubevirt.io", Resource: "virtualmachines", Namespace: "team-a-dev", Name: name}
	}
	for _, tc := range []struct {
		name string
		need []changeset.Access
		want []string
	}{
		{"a running VM", []changeset.Access{vm("create", "web")}, []string{"update virtualmachines "}},
		{"a project namespace", []changeset.Access{{Verb: "create", Resource: "namespaces", Name: "team-a-dev"}}, []string{"update namespaces "}},
		{"a new VM", []changeset.Access{vm("create", "db")}, []string{"create virtualmachines "}},
		{"a replaced VM", []changeset.Access{vm("delete", "web"), vm("create", "web")}, []string{"create virtualmachines ", "delete virtualmachines "}},
	} {
		s.ssar = ttlcache.New[ssarVerdict](visibleTTL)
		if err := s.Authorize(id, tc.need); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := q.take(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: asked %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A RoleBinding is weighed by the apiserver under the caller's token and
// persisted nowhere: a dry-run create, its verdict cached; a create finding
// the binding is asked again as an update; in a namespace that does not exist
// yet, the verb and bind on the role are asked as reviews.
func TestAuthorizeWeighsARoleBindingByDryRun(t *testing.T) {
	fc := newFakeCluster(t)
	q := &questions{}
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		if token != tenantToken {
			return false
		}
		q.add(a)
		return a.Resource == "rolebindings" || a.Verb == "bind" && a.Name == "admin"
	}
	var mu sync.Mutex
	var calls, sent []string
	answer := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rb rbacv1.RoleBinding
		if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &rb); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+rb.Name+" dryRun="+r.URL.Query().Get("dryRun"))
		sent = append(sent, string(body))
		mu.Unlock()
		switch {
		case bearer(r) != tenantToken || rb.RoleRef.Name != "admin":
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
		case r.Method == http.MethodPost && rb.Name == "existing":
			writeStatus(w, http.StatusConflict, metav1.StatusReasonAlreadyExists)
		default:
			rb.TypeMeta = metav1.TypeMeta{Kind: "RoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"}
			writeObj(w, rb)
		}
	}
	const bindings = "/apis/rbac.authorization.k8s.io/v1/namespaces/team-a-dev/rolebindings"
	fc.on(http.MethodPost, bindings, answer)
	fc.on(http.MethodPut, bindings+"/existing", answer)
	s := fc.server(nil, Config{})
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	grant := func(ns, name, role string) changeset.Access {
		raw, _ := json.Marshal(rbacv1.RoleBinding{
			TypeMeta:   metav1.TypeMeta{Kind: "RoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: "User", Name: "bob"}},
		})
		return changeset.Access{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: ns, Name: name, Object: string(raw)}
	}
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}

	if err := s.Authorize(id, []changeset.Access{grant("team-a-dev", "owners", "admin")}); err != nil {
		t.Fatalf("a grant the apiserver allows: %v", err)
	}
	if got := took(); !slices.Equal(got, []string{"POST owners dryRun=All"}) {
		t.Errorf("calls %v, want one dry-run create", got)
	}
	if mu.Lock(); len(sent) != 1 || sent[0] != grant("team-a-dev", "owners", "admin").Object {
		t.Errorf("sent %q, want the binding's own bytes", sent)
	}
	mu.Unlock()
	if err := s.Authorize(id, []changeset.Access{grant("team-a-dev", "owners", "admin")}); err != nil || len(took()) != 0 {
		t.Errorf("a repeat: %v, want the cached verdict", err)
	}
	if got := q.take(); len(got) != 0 {
		t.Errorf("a dry run asked reviews too: %v", got)
	}

	err := s.Authorize(id, []changeset.Access{grant("team-a-dev", "root", "cluster-admin")})
	if !errors.Is(err, model.ErrForbidden) || !strings.Contains(err.Error(), "create rolebindings root in team-a-dev") {
		t.Errorf("an escalation: %v", err)
	}

	if err := s.Authorize(id, []changeset.Access{grant("team-a-dev", "existing", "admin")}); err != nil {
		t.Errorf("an existing binding: %v", err)
	}
	if got := took(); !slices.Equal(got, []string{"POST root dryRun=All", "POST existing dryRun=All", "PUT existing dryRun=All"}) {
		t.Errorf("calls %v, want the existing binding asked again as an update", got)
	}

	if err := s.Authorize(id, []changeset.Access{grant("fresh", "owners", "admin")}); err != nil {
		t.Errorf("a binding in a new namespace: %v", err)
	}
	if got := q.take(); !slices.Equal(got, []string{"bind clusterroles admin", "create rolebindings "}) {
		t.Errorf("asked %v, want the verb and bind on the role", got)
	}
	if err := s.Authorize(id, []changeset.Access{grant("fresh", "root", "cluster-admin")}); !errors.Is(err, model.ErrForbidden) {
		t.Errorf("an escalation in a new namespace: %v", err)
	}
}

// A create the snapshot cannot place is dry-run as the caller, the exact
// bytes sent: an existing object (a namespace without the project label) is
// asked as the update Argo makes of it, so a caller who may create
// namespaces but not update them cannot join one; a new one is a create;
// the verdict is cached.
func TestAuthorizeDryRunsACreateTheSnapshotCannotPlace(t *testing.T) {
	fc := newFakeCluster(t)
	q := &questions{}
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		if token != tenantToken {
			return false
		}
		q.add(a)
		return a.Verb != "update"
	}
	var mu sync.Mutex
	var bodies []string
	created := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Query().Get("dryRun")+" "+string(body))
		mu.Unlock()
		if strings.Contains(string(body), `"existing"`) {
			writeStatus(w, http.StatusConflict, metav1.StatusReasonAlreadyExists)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
	fc.on(http.MethodPost, "/api/v1/namespaces", created)
	fc.on(http.MethodPost, "/apis/cdi.kubevirt.io/v1beta1/namespaces/team-a-dev/datavolumes", created)
	s := fc.server(nil, Config{})
	id := auth.Identity{Token: tenantToken, Username: "tenant"}
	namespace := func(name string) changeset.Access {
		return changeset.Access{Verb: "create", Resource: "namespaces", Name: name,
			Object: `{"apiVersion":"v1","kind":"Namespace","metadata":{"labels":{"dotvirt.io/project":"team-b"},"name":"` + name + `"}}`}
	}
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := bodies
		bodies = nil
		return out
	}

	err := s.Authorize(id, []changeset.Access{namespace("existing")})
	if !errors.Is(err, model.ErrForbidden) || !strings.Contains(err.Error(), "namespaces existing") {
		t.Errorf("joining an existing namespace without update: %v", err)
	}
	if got := took(); len(got) != 1 || got[0] != "All "+namespace("existing").Object {
		t.Errorf("sent %q, want the object's own bytes, dry run", got)
	}
	if got := q.take(); !slices.Equal(got, []string{"update namespaces ", "update namespaces existing"}) {
		t.Errorf("asked %v, want the update", got)
	}

	if err := s.Authorize(id, []changeset.Access{namespace("fresh")}); err != nil {
		t.Errorf("a new namespace: %v", err)
	}
	disk := changeset.Access{Verb: "create", Group: "cdi.kubevirt.io", Resource: "datavolumes", Namespace: "team-a-dev", Name: "disk",
		Object: `{"apiVersion":"cdi.kubevirt.io/v1beta1","kind":"DataVolume","metadata":{"name":"disk","namespace":"team-a-dev"}}`}
	for range 2 {
		if err := s.Authorize(id, []changeset.Access{disk}); err != nil {
			t.Errorf("a DataVolume: %v", err)
		}
	}
	if got := took(); len(got) != 2 {
		t.Errorf("dry runs %q, want one per new object, the repeat cached", got)
	}
}

// The role a binding grants is read as the apiserver reads it: by its exact
// key, never a differently cased decoy.
func TestRoleRefIsReadCaseSensitively(t *testing.T) {
	kind, name := roleRefOf(`{"roleref":{"kind":"ClusterRole","name":"view"},"roleRef":{"kind":"ClusterRole","name":"cluster-admin"}}`)
	if kind != "ClusterRole" || name != "cluster-admin" {
		t.Errorf("roleRef = %s %s, want ClusterRole cluster-admin", kind, name)
	}
}
