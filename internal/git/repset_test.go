package git

import (
	"context"
	"testing"
	"time"
)

func TestRepoSetCachesByURL(t *testing.T) {
	bareA := seedRepo(t)
	bareB := seedRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour) // long interval: poll won't fire in-test

	read1, write1, err := rs.Get(bareA)
	if err != nil {
		t.Fatalf("Get(A): %v", err)
	}
	if read1 == nil || write1 == nil {
		t.Fatal("Get returned nil read/write")
	}

	// Same URL → same cached instances.
	read2, write2, err := rs.Get(bareA)
	if err != nil {
		t.Fatalf("Get(A) again: %v", err)
	}
	if read1 != read2 || write1 != write2 {
		t.Error("RepoSet did not cache: same URL returned different instances")
	}

	// Different URL → different instances.
	readB, _, err := rs.Get(bareB)
	if err != nil {
		t.Fatalf("Get(B): %v", err)
	}
	if readB == read1 {
		t.Error("different URLs returned the same read repo")
	}

	// The read view actually works (proves Get opened a usable mirror).
	if _, err := read1.VMManifests("main"); err != nil {
		t.Errorf("cached read repo unusable: %v", err)
	}
}

func TestRepoSetGetBadURLErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs := NewRepoSet(ctx, "", nil, false, nil, time.Hour)

	if _, _, err := rs.Get("/nonexistent/repo.git"); err == nil {
		t.Error("expected an error opening a nonexistent repo")
	}
}
