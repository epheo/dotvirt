package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/tasks"
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

	got := scopeTasks(ops, merges, projects, false)
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
	got = scopeTasks(ops, nil, projects, true)
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
	if m.RepoURL != "https://forge/o/r" || m.Number != 12 || m.By != "alice" || m.Title != "resize web" {
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
