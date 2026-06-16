package changeset

import "testing"

func TestValidName(t *testing.T) {
	// Rejected: traversal, separators, uppercase, bad edges, spaces, over-length.
	bad := []string{"", "../x", "a/b", "..", "Team-A", "x..y", "-x", "x-", "a b", string(make([]byte, 64))}
	for _, s := range bad {
		if validName(s) {
			t.Errorf("validName(%q) = true, want false", s)
		}
	}
	// Accepted: DNS-1123 labels.
	for _, s := range []string{"team-a", "tenant-c", "a", "x1", "abc-123"} {
		if !validName(s) {
			t.Errorf("validName(%q) = false, want true", s)
		}
	}
}

func TestSiblingRepoURL(t *testing.T) {
	got := siblingRepoURL("https://forge/dotvirt/platform.git", "team-a")
	if want := "https://forge/dotvirt/team-a.git"; got != want {
		t.Errorf("siblingRepoURL = %q, want %q", got, want)
	}
	if got := siblingRepoURL("noslash", "x"); got != "" {
		t.Errorf("siblingRepoURL(no slash) = %q, want empty", got)
	}
}
