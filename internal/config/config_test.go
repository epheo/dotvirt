package config

import (
	"strings"
	"testing"
)

// An origin the API's cross-origin check would reject fails at startup, not as
// a 403 on every UI write.
func TestLoadRejectsInvalidUIOrigin(t *testing.T) {
	for _, o := range []string{"http://localhost:5173/", "http://localhost:5173/app", "*"} {
		if _, err := Load([]string{"-ui-origin", o, "-session-secret", "s"}); err == nil {
			t.Errorf("ui-origin %q accepted", o)
		}
	}
	for _, o := range []string{"", "http://localhost:5173"} {
		if _, err := Load([]string{"-ui-origin", o, "-session-secret", "s"}); err != nil {
			t.Errorf("ui-origin %q rejected: %v", o, err)
		}
	}
}

// A project label the snapshot could not select by fails at startup, naming
// the flag, instead of reading as a cluster with no projects.
func TestLoadRejectsInvalidProjectLabel(t *testing.T) {
	for _, l := range []string{"", "dotvirt.io/project=x", "bad key", "a/b/c"} {
		_, err := Load([]string{"-project-label", l, "-session-secret", "s"})
		if err == nil || !strings.Contains(err.Error(), "-project-label") {
			t.Errorf("project-label %q: err %v, want one naming the flag", l, err)
		}
	}
	if _, err := Load([]string{"-project-label", "dotvirt.io/project", "-session-secret", "s"}); err != nil {
		t.Errorf("the default label rejected: %v", err)
	}
}

// What tenant repos may declare comes from the operator: the listed kinds by
// default, Any with the provider's additions on top of dotvirt's own list. A
// value it cannot read fails at startup rather than disagree with the
// AppProjects.
func TestLoadReadsTheTenantKinds(t *testing.T) {
	c, err := Load([]string{"-session-secret", "s"})
	if err != nil || c.TenantKinds.Any {
		t.Fatalf("default: %+v, %v", c.TenantKinds, err)
	}
	c, err = Load([]string{"-session-secret", "s", "-tenant-kinds", "Any", "-tenant-kinds-denied", "capsule.clastix.io/*"})
	if err != nil || !c.TenantKinds.Any || c.TenantKinds.Declarable("capsule.clastix.io", "Tenant", false) ||
		c.TenantKinds.Declarable("rbac.authorization.k8s.io", "Role", false) || !c.TenantKinds.Declarable("apps", "Deployment", false) {
		t.Errorf("any: %+v, %v", c.TenantKinds, err)
	}
	for _, bad := range [][]string{
		{"-tenant-kinds", "All"},
		{"-tenant-kinds", "Any", "-tenant-kinds-denied", "Deployment"},
		{"-tenant-kinds-denied", "apps/Deployment"},
	} {
		if _, err := Load(append([]string{"-session-secret", "s"}, bad...)); err == nil || !strings.Contains(err.Error(), "-tenant-kinds") {
			t.Errorf("%v: err %v, want one naming the flag", bad, err)
		}
	}
}
