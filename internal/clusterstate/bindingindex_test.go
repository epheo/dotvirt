package clusterstate

import (
	"maps"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func binding(ns, name string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Subjects: subjects}
}

func userSub(name string) rbacv1.Subject  { return rbacv1.Subject{Kind: rbacv1.UserKind, Name: name} }
func groupSub(name string) rbacv1.Subject { return rbacv1.Subject{Kind: rbacv1.GroupKind, Name: name} }

func nsSet(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func TestBindingIndexFindsUsersGroupsAndServiceAccounts(t *testing.T) {
	x := newBindingIndex()
	x.put(binding("a", "owners", userSub("alice")))
	x.put(binding("b", "team", groupSub("devs")))
	x.put(binding("c", "bot", rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "ci"}))
	x.put(binding("d", "shared-bot", rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "c", Name: "ci"}))
	if got := x.namespaces("alice", []string{"devs"}); !maps.Equal(got, nsSet("a", "b")) {
		t.Errorf("alice = %v", got)
	}
	if got := x.namespaces("system:serviceaccount:c:ci", nil); !maps.Equal(got, nsSet("c", "d")) {
		t.Errorf("the ServiceAccount = %v", got)
	}
	if got := x.namespaces("mallory", []string{"others"}); len(got) != 0 {
		t.Errorf("a stranger = %v", got)
	}
}

// One namespace can bind a subject twice; losing one keeps it bound.
func TestBindingIndexCountsAndForgets(t *testing.T) {
	x := newBindingIndex()
	x.put(binding("a", "one", userSub("alice")))
	x.put(binding("a", "two", userSub("alice")))
	x.mu.Lock()
	x.dropLocked("a/one")
	x.mu.Unlock()
	if got := x.namespaces("alice", nil); !maps.Equal(got, nsSet("a")) {
		t.Errorf("after one of two = %v", got)
	}
	x.put(binding("a", "two", userSub("bob"))) // the update moves the subject
	if got := x.namespaces("alice", nil); len(got) != 0 {
		t.Errorf("alice after the update = %v", got)
	}
	if got := x.namespaces("bob", nil); !maps.Equal(got, nsSet("a")) {
		t.Errorf("bob after the update = %v", got)
	}
	if len(x.bySubject["u:alice"]) != 0 {
		t.Errorf("alice left behind: %v", x.bySubject)
	}
}

func TestBindingIndexReplaceStartsOver(t *testing.T) {
	x := newBindingIndex()
	x.put(binding("gone", "old", userSub("alice")))
	x.replace([]any{binding("kept", "new", userSub("alice"))})
	if got := x.namespaces("alice", nil); !maps.Equal(got, nsSet("kept")) {
		t.Errorf("after a relist = %v", got)
	}
}

// A move published to the bus must find the index already current.
func TestTheIndexMovesBeforeTheSignal(t *testing.T) {
	x := newBindingIndex()
	var seen map[string]bool
	sig := newRBACSignal(func(string) { seen = x.namespaces("alice", nil) }, nil, roleBindingDigest)
	sink := indexedBindings{sig, x}
	if err := sink.Add(binding("a", "owners", userSub("alice"))); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(seen, nsSet("a")) {
		t.Errorf("at the add's signal: %v", seen)
	}
	rb := binding("a", "owners", userSub("alice"))
	if err := sink.Delete(cache.DeletedFinalStateUnknown{Key: "a/owners", Obj: rb}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Errorf("at the delete's signal: %v", seen)
	}
}
