package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

func TestScopeTasks(t *testing.T) {
	now := time.Now()
	ops := []tasks.Op{
		{Verb: "Restart", Namespace: "team-a-ns", Name: "web", By: "alice", OK: true, At: now},
		{Verb: "Pause", Namespace: "secret-ns", Name: "db", By: "bob", OK: true, At: now},
		{Verb: "Cordon", Name: "worker-1", By: "carol", OK: true, At: now},
	}
	merges := []tasks.Merge{
		{RepoURL: "https://forge/o/team-a", Number: 4, Title: "add vm", By: "alice", At: now},
		{RepoURL: "https://forge/o/other", Number: 9, Title: "hidden", By: "eve", At: now},
	}
	projects := []project.ProjectInfo{
		{Name: "team-a", Repo: "https://forge/o/team-a.git", Namespaces: []string{"team-a-ns"}},
	}

	got := scopeTasks(ops, merges, newTaskScope(projects, false))
	if len(got) != 2 {
		t.Fatalf("want the visible op + merge only, got %+v", got)
	}
	for _, e := range got {
		switch e.Kind {
		case "op":
			if e.Verb != "Restart" || e.Project != "team-a" || e.By != "alice" {
				t.Errorf("op row = %+v", e)
			}
		case "merge":
			if e.PRNumber != 4 || e.Project != "team-a" || e.By != "alice" {
				t.Errorf("merge row = %+v", e)
			}
		}
	}

	// The node-read signal reveals node-scoped ops; namespace scoping is untouched.
	got = scopeTasks(ops, nil, newTaskScope(projects, true))
	if len(got) != 2 {
		t.Fatalf("node signal: got %+v", got)
	}
}

// A merged-PR webhook delivery must land in the feed instantly, attributed to
// the proposing user parsed from the dotvirt head branch (the poster is the bot).
func TestWebhookRecordsMerge(t *testing.T) {
	feed := tasks.New(eventbus.New())
	s := NewServer(Deps{
		Tasks:  feed,
		Reader: &fakeDraft{},
		Draft:  &fakeDraft{},
		Config: Config{WebhookSecret: "hooksecret", BaseBranch: "main", ProposedBranch: "dotvirt/proposed"},
	})
	// merged_at must be fresh: the feed prunes past MergeRetention on write.
	body := []byte(`{
		"action": "closed",
		"pull_request": {
			"number": 12, "title": "resize web", "html_url": "https://forge/o/r/pulls/12",
			"merged": true, "merged_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
			"user": {"login": "dotvirt-bot"},
			"head": {"ref": "dotvirt/proposed/alice/team-a-1a2b3c"},
			"base": {"ref": "main"}
		},
		"repository": {"clone_url": "https://forge/o/r.git", "html_url": "https://forge/o/r"}
	}`)
	if w := deliver(t, s, body, sign(body, "hooksecret")); w.Code != http.StatusNoContent {
		t.Fatalf("delivery: got %d, want 204", w.Code)
	}
	ms := feed.Merges()
	if len(ms) != 1 {
		t.Fatalf("merges = %+v", ms)
	}
	m := ms[0]
	if m.RepoURL != "https://forge/o/r" || m.Number != 12 || m.By != "alice" || m.Title != "resize web" || m.Branch != "dotvirt/proposed/alice/team-a-1a2b3c" {
		t.Fatalf("merge = %+v", m)
	}

	// A close without merge records nothing.
	body = []byte(`{"action":"closed","pull_request":{"number":13,"merged":false,"base":{"ref":"main"}},
		"repository":{"clone_url":"https://forge/o/r.git"}}`)
	if w := deliver(t, s, body, sign(body, "hooksecret")); w.Code != http.StatusNoContent {
		t.Fatalf("delivery: got %d, want 204", w.Code)
	}
	if got := feed.Merges(); len(got) != 1 {
		t.Fatalf("unmerged close recorded: %+v", got)
	}
}

// A caller who may not read the platform repo sees, of its merges, exactly
// their own draft's, and their tasks watermark moves exactly when that feed
// does. A platform reader sees every one.
func TestOwnPlatformMergesInTheFeed(t *testing.T) {
	g, _ := newLaneRig(t)
	g.s.tasks = tasks.New(g.s.bus)
	repo := forge.NormalizeRepoURL(platformRepo)
	alice := auth.Identity{Token: g.token(nil), Username: "alice"}
	feed := func(id auth.Identity) (string, []string) {
		t.Helper()
		rec := doAs(g.s, id, http.MethodGet, "/api/tasks")
		wantStatus(t, rec, http.StatusOK)
		var rows []string
		for _, e := range decodeBody[[]model.TaskEntry](t, rec) {
			rows = append(rows, fmt.Sprintf("%s #%d", e.Project, e.PRNumber))
		}
		sort.Strings(rows)
		return rec.Body.String(), rows
	}
	mark := func(id auth.Identity) uint64 {
		t.Helper()
		inv, err := g.s.InventoryForIdentity(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return inv.TasksVersion
	}
	body, _ := feed(alice)
	version := mark(alice)
	at := time.Now()
	for _, c := range []struct {
		what   string
		merge  tasks.Merge
		shows  bool
		alices []string
	}{
		{"bob's merge", tasks.Merge{RepoURL: repo, Number: 2, Title: "b", Branch: "bob", At: at}, false, nil},
		{"alice's legacy revert", tasks.Merge{RepoURL: repo, Number: 3, Title: "l", Branch: "legacy-alice", At: at}, false, nil},
		{"alice's revert", tasks.Merge{RepoURL: repo, Number: 6, Title: "r", Branch: "revert/alice", At: at}, false, nil},
		{"a merge with no branch", tasks.Merge{RepoURL: repo, Number: 5, Title: "x", At: at}, false, nil},
		{"alice's merge", tasks.Merge{RepoURL: repo, Number: 1, Title: "a", Branch: "alice", At: at}, true, []string{"platform #1"}},
		{"alice's merge retitled", tasks.Merge{RepoURL: repo, Number: 1, Title: "a2", Branch: "alice", At: at}, true, []string{"platform #1"}},
	} {
		g.s.tasks.RecordMerge(c.merge)
		nextBody, rows := feed(alice)
		nextVersion := mark(alice)
		if moved := nextBody != body; moved != c.shows || (nextVersion != version) != moved {
			t.Errorf("%s: feed moved %v, watermark moved %v; want both %v", c.what, moved, nextVersion != version, c.shows)
		}
		if !slices.Equal(rows, c.alices) {
			t.Errorf("%s: alice's feed %q, want %q", c.what, rows, c.alices)
		}
		body, version = nextBody, nextVersion
	}
	if _, rows := feed(auth.Identity{Token: g.token(platformReader), Username: "carol"}); !slices.Equal(rows, []string{"platform #1", "platform #2", "platform #3", "platform #5", "platform #6"}) {
		t.Errorf("reader's feed %q, want every platform merge", rows)
	}
}
