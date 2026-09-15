package git

import (
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// A view is built once per branch head: same head serves the memo, another
// view of the same branch builds on its own, a moved head rebuilds, and a
// failed build leaves nothing behind to serve.
func TestMemoizedByBranchHead(t *testing.T) {
	bare := seedRepo(t)
	r, err := Open(bare, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	builds := 0
	count := func() (int, error) { builds++; return builds, nil }
	get := func(view string) int {
		t.Helper()
		v, err := Memoized(r, view, "main", count)
		if err != nil {
			t.Fatalf("Memoized(%s): %v", view, err)
		}
		return v
	}

	if get("a") != 1 || get("a") != 1 {
		t.Fatalf("same head must be served from the memo, built %d times", builds)
	}
	if get("b") != 2 {
		t.Fatalf("another view of the branch must build on its own, got %d builds", builds)
	}

	w := OpenWrite(bare, "", nil, true)
	if _, err := w.Commit("main", "bump", []model.File{{Path: "alpha/new.yaml", Content: []byte("kind: ConfigMap\n")}}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := r.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if get("a") != 3 {
		t.Fatalf("a moved head must rebuild, got %d builds", builds)
	}

	boom := errors.New("boom")
	if _, err := Memoized(r, "c", "main", func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("build error must surface, got %v", err)
	}
	if v, _ := Memoized(r, "c", "main", count); v != 4 {
		t.Fatalf("a failed build must not be memoized, got %d", v)
	}
}
