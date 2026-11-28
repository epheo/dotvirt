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

// An answer the mirror cannot give yet keeps the last known one: Argo ahead
// of the mirror must not read yes, absent, yes, which would move a partial
// viewer's watermark when another tenant's merge applied. A known no stands
// until a known yes, and nothing is known before the first answer.
func TestAppliedKeepsTheLastKnownAnswer(t *testing.T) {
	s := &Server{tasks: tasks.New(nil)}
	mine := []project.ProjectInfo{{Name: "team-a", Namespaces: []string{"team-a-dev"}, Repo: "https://forge/team-a.git"}}
	var holds, known bool
	scope := func() taskScope {
		sc := newTaskScope(mine, false)
		sc.applied = func(m tasks.Merge) *bool { return s.applied.answer(m, holds, known) }
		return sc
	}
	s.tasks.RecordMerge(tasks.Merge{RepoURL: "https://forge/team-a", Number: 2, Commit: "c0ffee", Title: "ours", At: time.Now()})
	none := s.tasksWatermark(scope())
	if got := scopeTasks(nil, s.tasks.Merges(), scope()); got[0].Applied != nil {
		t.Fatalf("an answer before any was known: %v", *got[0].Applied)
	}
	var marks []uint64
	for _, step := range []struct{ holds, known bool }{{true, true}, {false, false}, {true, true}, {false, false}} {
		holds, known = step.holds, step.known
		marks = append(marks, s.tasksWatermark(scope()))
		if got := scopeTasks(nil, s.tasks.Merges(), scope()); got[0].Applied == nil || !*got[0].Applied {
			t.Fatalf("step %+v: applied = %v", step, got[0].Applied)
		}
	}
	if marks[0] == none || marks[1] != marks[0] || marks[2] != marks[0] || marks[3] != marks[0] {
		t.Errorf("watermarks %d then %v: want one move", none, marks)
	}
	holds, known = false, true
	if no := s.tasksWatermark(scope()); no == marks[0] {
		t.Error("a known no did not move the watermark")
	}
	holds, known = false, false
	if got := scopeTasks(nil, s.tasks.Merges(), scope()); got[0].Applied == nil || *got[0].Applied {
		t.Errorf("the known no did not stand: %v", got[0].Applied)
	}
}

// A merge carries whether Argo runs it, never the revision; the watermark
// moves when that answer does, so the console pulls the feed again.
func TestMergesCarryTheirAppliedVerdict(t *testing.T) {
	s := &Server{tasks: tasks.New(nil)}
	mine := []project.ProjectInfo{{Name: "team-a", Namespaces: []string{"team-a-dev"}, Repo: "https://forge/team-a.git"}}
	var answer *bool
	scope := func() taskScope {
		sc := newTaskScope(mine, false)
		sc.applied = func(tasks.Merge) *bool { return answer }
		return sc
	}
	s.tasks.RecordMerge(tasks.Merge{RepoURL: "https://forge/team-a", Number: 2, Title: "ours", At: time.Now()})
	unknown := s.tasksWatermark(scope())
	if got := scopeTasks(nil, s.tasks.Merges(), scope()); len(got) != 1 || got[0].Applied != nil {
		t.Fatalf("unknown verdict: %+v", got)
	}
	no, yes := false, true
	answer = &no
	pending := s.tasksWatermark(scope())
	answer = &yes
	live := s.tasksWatermark(scope())
	if unknown == pending || pending == live || unknown == live {
		t.Errorf("watermark did not follow the verdict: %d, %d, %d", unknown, pending, live)
	}
	if got := scopeTasks(nil, s.tasks.Merges(), scope()); got[0].Applied == nil || !*got[0].Applied {
		t.Errorf("applied merge row = %+v", got[0])
	}
}

// A merged-PR webhook delivery must land in the feed instantly, attributed to
// the proposing user parsed from the dotvirt head branch (the poster is the bot).
func TestWebhookRecordsMerge(t *testing.T) {
	feed := tasks.New(eventbus.New())
	s := NewServer(Deps{
		Tasks:  feed,
		Forge:  forge.NewFactory("https://forge", forge.StaticToken("t"), false, "").WithWebURL("https://web"),
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
			"merge_commit_sha": "c0ffee0000000000000000000000000000000012",
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
	// The link is the web base's, not the forge's html_url.
	if m.URL != "https://web/o/r/pulls/12" {
		t.Errorf("merge link = %q, want the web base's", m.URL)
	}
	if m.RepoURL != "https://forge/o/r" || m.Number != 12 || m.By != "alice" || m.Title != "resize web" || m.Branch != "dotvirt/proposed/alice/team-a-1a2b3c" {
		t.Fatalf("merge = %+v", m)
	}
	// The forge's own merge commit, the one Argo must run, whatever subject
	// later commits claim the PR with.
	if m.Commit != "c0ffee0000000000000000000000000000000012" {
		t.Errorf("merge commit = %q", m.Commit)
	}

	// Merged with its branch deleted: Forgejo reports the PR's own ref, and
	// the branch only in the label.
	body = []byte(`{
		"action": "closed",
		"pull_request": {
			"number": 14, "title": "resize db", "html_url": "https://forge/o/r/pulls/14",
			"merged": true, "merged_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
			"user": {"login": "dotvirt-bot"},
			"head": {"ref": "refs/pull/14/head", "label": "dotvirt/proposed/bob/team-a-4d5e6f"},
			"base": {"ref": "main"}
		},
		"repository": {"clone_url": "https://forge/o/r.git", "html_url": "https://forge/o/r"}
	}`)
	if w := deliver(t, s, body, sign(body, "hooksecret")); w.Code != http.StatusNoContent {
		t.Fatalf("delivery: got %d, want 204", w.Code)
	}
	if ms = feed.Merges(); len(ms) != 2 {
		t.Fatalf("merges = %+v", ms)
	}
	for _, m := range ms {
		if m.Number == 14 && (m.By != "bob" || m.Branch != "dotvirt/proposed/bob/team-a-4d5e6f") {
			t.Fatalf("deleted-branch merge = %+v", m)
		}
	}

	// A close without merge records nothing.
	body = []byte(`{"action":"closed","pull_request":{"number":13,"merged":false,"base":{"ref":"main"}},
		"repository":{"clone_url":"https://forge/o/r.git"}}`)
	if w := deliver(t, s, body, sign(body, "hooksecret")); w.Code != http.StatusNoContent {
		t.Fatalf("delivery: got %d, want 204", w.Code)
	}
	if got := feed.Merges(); len(got) != 2 {
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

// An op acts on a VM, or on a desktop: it shows where the caller may list
// what it acted on.
func TestOpsFollowTheRightOnWhatTheyActOn(t *testing.T) {
	sc := newTaskScope([]project.ProjectInfo{{Name: "p", Namespaces: []string{"a", "b"}}}, false)
	sc.lists = func(ref ssarRef, ns string) bool {
		return ref == ssarVM && ns == "a" || ref == ssarDesktops && ns == "b"
	}
	if _, ok := sc.op(tasks.Op{Namespace: "a"}); !ok {
		t.Error("an op where the caller lists VMs is hidden")
	}
	if _, ok := sc.op(tasks.Op{Namespace: "b"}); ok {
		t.Error("an op where the caller does not list VMs shows")
	}
	if _, ok := sc.op(tasks.Op{Namespace: "b", Desktop: true}); !ok {
		t.Error("an op on a desktop where the caller lists desktops is hidden")
	}
	if _, ok := sc.op(tasks.Op{Namespace: "a", Desktop: true}); ok {
		t.Error("an op on a desktop where the caller lists only VMs shows")
	}
}
