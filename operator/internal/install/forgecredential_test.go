package install

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	dotvirtv1alpha1 "github.com/epheo/dotvirt/operator/api/v1alpha1"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// The grant covers the one Secret spec.forge.credentialsSecret names, in the
// install namespace, for the operator's own account: read for a bring-your-own
// forge, read and write for a managed one, which mints the token into it.
func TestForgeCredentialGrant(t *testing.T) {
	for _, c := range []struct {
		name    string
		managed bool
		verbs   []string
	}{
		{"bring your own", false, []string{"get"}},
		{"managed", true, []string{"get", "create", "patch"}},
	} {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt", UID: "uid-x"}}
		dv.Spec.Forge.CredentialsSecret, dv.Spec.Forge.Managed = "byo-creds", c.managed
		role, binding := ForgeCredentialGrant(dv, "dotvirt-operator")
		if role == nil || binding == nil {
			t.Fatalf("%s: no grant for a custom credential", c.name)
		}
		if role.Name != ForgeCredentialGrantName || role.Namespace != "dotvirt" || binding.Name != ForgeCredentialGrantName || binding.Namespace != "dotvirt" {
			t.Errorf("%s: grant at %s/%s and %s/%s, want %s in the install namespace", c.name,
				role.Namespace, role.Name, binding.Namespace, binding.Name, ForgeCredentialGrantName)
		}
		want := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"byo-creds"}, Verbs: c.verbs}}
		if !slices.EqualFunc(role.Rules, want, func(a, b rbacv1.PolicyRule) bool {
			return slices.Equal(a.APIGroups, b.APIGroups) && slices.Equal(a.Resources, b.Resources) &&
				slices.Equal(a.ResourceNames, b.ResourceNames) && slices.Equal(a.Verbs, b.Verbs)
		}) {
			t.Errorf("%s: rules = %+v, want %+v", c.name, role.Rules, want)
		}
		// Owned by the CR without blockOwnerDeletion: the garbage collector takes
		// the grant with the install, and whoever installs it needs no finalizers right.
		for _, owners := range [][]metav1.OwnerReference{role.OwnerReferences, binding.OwnerReferences} {
			if len(owners) != 1 || owners[0].Kind != "Dotvirt" || owners[0].Name != "x" || owners[0].UID != "uid-x" ||
				owners[0].BlockOwnerDeletion != nil || owners[0].Controller != nil {
				t.Errorf("%s: owners = %+v, want the CR alone, not blocking its deletion", c.name, owners)
			}
		}
		wantRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: ForgeCredentialGrantName}
		wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: OperatorServiceAccount, Namespace: "dotvirt-operator"}}
		if binding.RoleRef != wantRef || !slices.Equal(binding.Subjects, wantSubjects) {
			t.Errorf("%s: binding = %+v %+v, want the Role to the operator's ServiceAccount", c.name, binding.RoleRef, binding.Subjects)
		}
	}
}

// No grant where the operator's own role already covers the name, or where it
// cannot name its own account.
func TestForgeCredentialGrantOnlyWhenNeeded(t *testing.T) {
	for _, c := range []struct {
		secret, operatorNS string
	}{
		{"", "dotvirt-operator"},
		{"dotvirt-forge", "dotvirt-operator"},
		{"byo-creds", ""},
	} {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
		dv.Spec.Forge.CredentialsSecret = c.secret
		if role, binding := ForgeCredentialGrant(dv, c.operatorNS); role != nil || binding != nil {
			t.Errorf("credentialsSecret %q, operator namespace %q: a grant was rendered", c.secret, c.operatorNS)
		}
	}
}

// The commands an admin runs by hand grant what ForgeCredentialGrant renders.
func TestForgeCredentialCommands(t *testing.T) {
	dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt"}}
	dv.Spec.Forge.CredentialsSecret = "byo-creds"
	want := []string{
		"kubectl -n dotvirt create role dotvirt-forge-credential --verb=get --resource=secrets --resource-name=byo-creds",
		"kubectl -n dotvirt create rolebinding dotvirt-forge-credential --role=dotvirt-forge-credential --serviceaccount=dotvirt-operator:dotvirt-operator-controller",
	}
	if got := ForgeCredentialCommands(dv, "dotvirt-operator"); !slices.Equal(got, want) {
		t.Errorf("bring your own: %q, want %q", got, want)
	}
	// A half grant is replaced too: the delete tolerates the one that is absent.
	if got := ForgeCredentialReplaceCommands(dv, "dotvirt-operator"); len(got) != 3 ||
		got[0] != "kubectl -n dotvirt delete role,rolebinding dotvirt-forge-credential --ignore-not-found" || !slices.Equal(got[1:], want) {
		t.Errorf("replace: %q", got)
	}
	dv.Spec.Forge.Managed = true
	if got := ForgeCredentialCommands(dv, ""); !strings.Contains(got[0], "--verb=get,create,patch") || !strings.Contains(got[1], "<operator-namespace>:") {
		t.Errorf("managed, operator namespace unknown: %q", got)
	}
}

// The operator's grant is the console's render of it, one source: the Role and
// RoleBinding in adminobjects.Render for the pins the console reads from the
// same install (its spec, its name and uid, the operator's account) are the
// operator's, object for object, and absent exactly where it renders none.
func TestForgeCredentialGrantIsTheConsoles(t *testing.T) {
	for _, c := range []struct {
		secret  string
		managed bool
	}{
		{"", false}, {"dotvirt-forge", true}, {SessionSecretName, true}, {"creds", false}, {"creds", true},
	} {
		dv := &dotvirtv1alpha1.Dotvirt{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "dotvirt", UID: "uid-x"}}
		dv.Spec.Forge.CredentialsSecret, dv.Spec.Forge.Managed = c.secret, c.managed
		// As the console's adminParams read them.
		console := adminobjects.Params{
			Operator: "system:serviceaccount:dotvirt-operator:" + OperatorServiceAccount, Namespace: dv.Namespace,
			ForgeSecret: adminobjects.ForgeSecret(dv.Spec.Forge.CredentialsSecret), ForgeManaged: dv.Spec.Forge.Managed,
			InstallName: dv.Name, InstallUID: dv.UID,
		}
		var rendered []any
		for _, o := range adminobjects.Render(console) {
			if o.Object.GetName() == ForgeCredentialGrantName {
				rendered = append(rendered, o.Object.Object)
			}
		}
		var operator []any
		if role, binding := ForgeCredentialGrant(dv, "dotvirt-operator"); role != nil {
			for _, obj := range []runtime.Object{role, binding} {
				m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
				if err != nil {
					t.Fatal(err)
				}
				unstructured.RemoveNestedField(m, "metadata", "creationTimestamp")
				operator = append(operator, m)
			}
		}
		if !reflect.DeepEqual(rendered, operator) {
			t.Errorf("credentialsSecret %q managed %v:\nconsole  %v\noperator %v", c.secret, c.managed, rendered, operator)
		}
		if needs := ForgeCredentialNeedsGrant(dv); needs != (len(operator) > 0) {
			t.Errorf("credentialsSecret %q: needs a grant %v, renders %d objects", c.secret, needs, len(operator))
		}
	}
}
