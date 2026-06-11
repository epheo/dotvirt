package forge

import "testing"

func TestOwnerRepo(t *testing.T) {
	cases := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"https://forge.example/dotvirt/team-a.git", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a/", "dotvirt", "team-a", true},
		{"http://forgejo-http.forgejo.svc:3000/dotvirt/vmrepo.git", "dotvirt", "vmrepo", true},
		// Query string must not leak into the repo name (fails closed otherwise).
		{"https://forge.example/dotvirt/team-a.git?ref=main", "dotvirt", "team-a", true},
		{"https://forge.example/dotvirt/team-a?x=1#frag", "dotvirt", "team-a", true},
		// Nested groups: last two segments win.
		{"https://forge.example/org/sub/team-a.git", "sub", "team-a", true},
		// Unparseable → fail closed.
		{"https://forge.example/onlyone", "", "", false},
		{"not-a-url", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := ownerRepo(c.url)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("ownerRepo(%q) = (%q,%q,%v), want (%q,%q,%v)", c.url, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}
