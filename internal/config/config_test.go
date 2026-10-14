package config

import "testing"

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
