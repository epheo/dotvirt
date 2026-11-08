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
