package git

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// TestParseVMsOnBranchMemoizes proves the parse is served from cache while the branch
// hash is unchanged, and re-parsed when it differs — so the whole-tree walk runs once
// per content change, not once per inventory build.
func TestParseVMsOnBranchMemoizes(t *testing.T) {
	bare := seedRepo(t) // one VirtualMachine (web) on main
	r, err := Open(bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	vms, err := r.ParseVMsOnBranch("main")
	if err != nil {
		t.Fatalf("ParseVMsOnBranch: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("want 1 VM on main, got %d", len(vms))
	}
	cached, ok := r.parseCache["main"]
	if !ok || cached.hash == "" || cached.hash != r.branchHash("main") {
		t.Fatalf("parse not cached under the branch hash: %+v", cached)
	}

	// A same-hash read is a cache hit: seed a sentinel and confirm it's returned.
	sentinel := []model.VM{{Name: "sentinel"}}
	r.parseCache["main"] = branchParse{hash: r.branchHash("main"), vms: sentinel}
	if got, _ := r.ParseVMsOnBranch("main"); len(got) != 1 || got[0].Name != "sentinel" {
		t.Errorf("same-hash read did not return the memoized value: %+v", got)
	}

	// A hash mismatch (stale entry) forces a re-parse (the real VM, not the sentinel).
	r.parseCache["main"] = branchParse{hash: "stale", vms: sentinel}
	got, _ := r.ParseVMsOnBranch("main")
	if len(got) == 1 && got[0].Name == "sentinel" {
		t.Error("a stale-hash entry was served instead of re-parsing")
	}
}
