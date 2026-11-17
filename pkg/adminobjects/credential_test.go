package adminobjects

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func credentialParams(secret string, managed bool) Params {
	return Params{
		Operator: "system:serviceaccount:dotvirt-operator:dotvirt-operator-controller", Namespace: "dotvirt",
		ForgeSecret: ForgeSecret(secret), ForgeManaged: managed, InstallName: "x", InstallUID: "uid-x",
	}
}

// The grant is rendered exactly where the operator's own role does not name
// the credential: never for the default, never for a Secret the operator
// generates for itself (refused as NameTaken), always for an admin's.
func TestCredentialGrantOnlyForAnAdminsSecret(t *testing.T) {
	for _, c := range []struct {
		secret       string
		grant, taken bool
	}{
		{"", false, false},
		{DefaultForgeSecret, false, false},
		{SessionSecret, false, true},
		{ArgoSecret, false, true},
		{"creds", true, false},
	} {
		p := credentialParams(c.secret, false)
		if got := CredentialNeedsGrant(p.ForgeSecret); got != c.grant {
			t.Errorf("%q: needs a grant %v, want %v", c.secret, got, c.grant)
		}
		if got := CredentialNameTaken(p.ForgeSecret); got != c.taken {
			t.Errorf("%q: name taken %v, want %v", c.secret, got, c.taken)
		}
		rendered := slices.ContainsFunc(Render(p), func(o Object) bool { return o.Object.GetName() == CredentialGrantName })
		if rendered != c.grant {
			t.Errorf("%q: Render includes the grant %v, want %v", c.secret, rendered, c.grant)
		}
	}
	if role, binding := CredentialGrant(Params{Namespace: "dotvirt", ForgeSecret: "creds"}); role != nil || binding != nil {
		t.Error("a grant was rendered for an unknown operator")
	}
}

// The grant: the credential's verbs on that one Secret in the install
// namespace, bound to the operator's account, owned by the install without
// blocking its deletion.
func TestCredentialGrantShape(t *testing.T) {
	for _, managed := range []bool{false, true} {
		role, binding := CredentialGrant(credentialParams("creds", managed))
		want := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"creds"}, Verbs: CredentialVerbs(managed)}}
		if role.Name != CredentialGrantName || role.Namespace != "dotvirt" || !slices.EqualFunc(role.Rules, want, sameRule) {
			t.Errorf("managed=%v: Role %s/%s %+v", managed, role.Namespace, role.Name, role.Rules)
		}
		if managed != slices.Contains(role.Rules[0].Verbs, "patch") || managed != slices.Contains(role.Rules[0].Verbs, "create") {
			t.Errorf("managed=%v: verbs %v", managed, role.Rules[0].Verbs)
		}
		if binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: CredentialGrantName}) ||
			!slices.Equal(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Name: "dotvirt-operator-controller", Namespace: "dotvirt-operator"}}) {
			t.Errorf("managed=%v: RoleBinding %+v %+v", managed, binding.RoleRef, binding.Subjects)
		}
		for _, owner := range append(role.OwnerReferences, binding.OwnerReferences...) {
			if owner.Kind != "Dotvirt" || owner.Name != "x" || owner.UID != "uid-x" || owner.BlockOwnerDeletion != nil || owner.Controller != nil {
				t.Errorf("managed=%v: owner %+v", managed, owner)
			}
		}
		if len(role.OwnerReferences) != 1 || len(binding.OwnerReferences) != 1 {
			t.Errorf("managed=%v: owners %v %v", managed, role.OwnerReferences, binding.OwnerReferences)
		}
	}
}

// The console applies only what Allowed names: the grant's Role and
// RoleBinding in the install namespace, nowhere else.
func TestAllowedNamesTheCredentialGrant(t *testing.T) {
	allowed := Allowed("dotvirt")
	for _, o := range Render(credentialParams("creds", true)) {
		if _, ok := Allow(allowed, o.Object); !ok {
			t.Errorf("%s %s/%s is rendered but not allowed", o.Object.GetKind(), o.Object.GetNamespace(), o.Object.GetName())
		}
	}
	elsewhere := &unstructured.Unstructured{}
	elsewhere.SetAPIVersion("rbac.authorization.k8s.io/v1")
	elsewhere.SetKind("RoleBinding")
	elsewhere.SetNamespace("kube-system")
	elsewhere.SetName(CredentialGrantName)
	if _, ok := Allow(allowed, elsewhere); ok {
		t.Error("the grant's RoleBinding is allowed outside the install namespace")
	}
}

// A grant made with the kubectl commands reads as installed; one granting
// more, less, another Secret or another account reads as outdated, saying how.
func TestCompareTheCredentialGrant(t *testing.T) {
	p := credentialParams("creds", true)
	role, binding := CredentialGrant(p)
	want := map[string]*unstructured.Unstructured{"Role": toUnstructured(role), "RoleBinding": toUnstructured(binding)}
	// As kubectl create writes them: no owner, no labels, the verbs in the
	// order the admin typed them.
	byHand := func(kind string) *unstructured.Unstructured {
		o := want[kind].DeepCopy()
		o.SetOwnerReferences(nil)
		o.SetLabels(nil)
		if kind == "Role" {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"},
				"resourceNames": []any{"creds"}, "verbs": []any{"patch", "get", "create"}}}, "rules")
		}
		return o
	}
	for _, kind := range []string{"Role", "RoleBinding"} {
		if s, d := Compare(want[kind], byHand(kind)); s != Installed {
			t.Errorf("%s by hand: %s (%s)", kind, s, d)
		}
	}
	for name, change := range map[string]func(*unstructured.Unstructured){
		"Role granting every Secret": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get"}}}, "rules")
		},
		"Role granting more verbs": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"creds"}, "verbs": []any{"get", "create", "patch", "list"}}}, "rules")
		},
		// A managed forge's grant that reads but cannot store the minted token.
		"Role granting read alone": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"creds"}, "verbs": []any{"get"}}}, "rules")
		},
		"RoleBinding to another account": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(o.Object, []any{map[string]any{"kind": "ServiceAccount", "name": "default", "namespace": "dotvirt-operator"}}, "subjects")
		},
		"RoleBinding to another role": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "admin", "roleRef", "name")
		},
	} {
		kind := strings.Fields(name)[0]
		live := byHand(kind)
		change(live)
		if s, d := Compare(want[kind], live); s != Outdated || d == "" {
			t.Errorf("%s: %s (%q), want Outdated saying how", name, s, d)
		}
	}
}

// The commands an admin runs without the console are a static template: no
// value in them, each variable derived from the cluster as the operator's
// README shows, which carries them verbatim.
func TestCredentialCommandsAreATemplate(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "operator", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{CredentialGrantCommands, CredentialStaleCommand} {
		if !strings.Contains(string(readme), cmd) {
			t.Errorf("the operator README does not carry\n%s", cmd)
		}
	}
	for _, v := range []string{"$INSTALL_NS", "$VERBS", "$CREDENTIAL", "$OPERATOR"} {
		if !strings.Contains(CredentialGrantCommands, v) {
			t.Errorf("the grant commands do not take %s", v)
		}
	}
}

// A grant left after the credential went back to the operator's own is a
// leftover, to report and remove; none while a grant is needed.
func TestLeftovers(t *testing.T) {
	if got := Leftovers(credentialParams("creds", false)); len(got) != 0 {
		t.Errorf("leftovers while the grant is needed: %v", got)
	}
	got := Leftovers(credentialParams("", false))
	if len(got) != 2 || got[0].Kind != "Role" || got[1].Kind != "RoleBinding" || got[0].Namespace != "dotvirt" || got[1].Name != CredentialGrantName {
		t.Errorf("leftovers for the default credential: %v", got)
	}
}
