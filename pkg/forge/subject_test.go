package forge

import "testing"

func TestMergeSubject(t *testing.T) {
	cases := []struct {
		in    string
		title string
		num   int
		ok    bool
	}{
		{"Merge pull request 'Resize web to 4 vCPU' (#12) from dotvirt/proposed/alice/p-1a2b into main", "Resize web to 4 vCPU", 12, true},
		{"Merge pull request 'it's (quoted)' (#7) from x into main", "it's (quoted)", 7, true},
		{"Resize web (#3)", "Resize web", 3, true},
		{"Revert \"Resize web\" (#3) (#4)", "Revert \"Resize web\" (#3)", 4, true},
		{"bump web to 4 cpu", "", 0, false},
		{"Merge branch 'feature' into main", "", 0, false},
		{" (#5)", "", 0, false},
	}
	for _, c := range cases {
		title, num, ok := MergeSubject(c.in)
		if ok != c.ok || title != c.title || num != c.num {
			t.Errorf("MergeSubject(%q) = (%q, %d, %v), want (%q, %d, %v)", c.in, title, num, ok, c.title, c.num, c.ok)
		}
	}
}
