package changeset

import (
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
)

func TestPRBodyRendersChangesAndRestartPlane(t *testing.T) {
	view := model.DraftView{Items: []model.DraftItem{
		{Kind: "edit", Namespace: "payments-prod", Name: "web-01", Changes: []model.Change{
			{Field: "CPU", Action: "change", From: "2 vCPU", To: "4 vCPU", Restart: true},
			{Field: "Label tier", Action: "add", To: "web"},
		}},
		{Kind: "delete", Namespace: "payments-prod", Name: "cache-01", Changes: []model.Change{
			{Field: "lifecycle", Action: "remove", From: "payments-prod/cache-01"},
		}},
	}, Warning: "Running but not in git: VM x/y."}

	body := prBody(view, "resize the web tier", "kube:admin", "dotvirt")
	for _, want := range []string{
		"resize the web tier",
		"**edit payments-prod/web-01**",
		"- CPU: 2 vCPU -> 4 vCPU *(restart required)*",
		"- Label tier: + web",
		"**delete payments-prod/cache-01**",
		"- lifecycle: - payments-prod/cache-01",
		"next power cycle",
		"**Warning:** Running but not in git: VM x/y.",
		"Proposed from dotvirt by kube:admin.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
}

func TestPRBodyNoRestartNoFooterNote(t *testing.T) {
	view := model.DraftView{Items: []model.DraftItem{
		{Kind: "edit", Namespace: "a", Name: "b", Changes: []model.Change{
			{Field: "Label x", Action: "add", To: "1"},
		}},
	}}
	body := prBody(view, "", "u", "dotvirt")
	if strings.Contains(body, "power cycle") {
		t.Errorf("restart note rendered without any restart-flagged change:\n%s", body)
	}
}

func TestPRBodyAcceptedDefaultIsNotAppendedTwice(t *testing.T) {
	view := model.DraftView{Items: []model.DraftItem{
		{Kind: "edit", Namespace: "a", Name: "b", Changes: []model.Change{
			{Field: "CPU", Action: "change", From: "2 vCPU", To: "4 vCPU"},
		}},
	}}
	edited := "Bump for the launch.\n\n" + prBody(view, "", "u", "dotvirt")
	if got := prBody(view, edited, "u", "dotvirt"); got != edited {
		t.Errorf("edited default body not kept verbatim:\n%s", got)
	}
	if n := strings.Count(prBody(view, edited, "u", "dotvirt"), "## Changes"); n != 1 {
		t.Errorf("changes block rendered %d times, want 1", n)
	}
}

// The footer names the product; the message is read back whatever name the
// footer carries.
func TestPRBodyFooterNamesTheProduct(t *testing.T) {
	view := model.DraftView{Items: []model.DraftItem{{Kind: "edit", Namespace: "a", Name: "b"}}}
	if body := prBody(view, "", "u", "Uniwatt Cloud"); !strings.HasSuffix(body, "\n---\nProposed from Uniwatt Cloud by u.\n") {
		t.Errorf("footer:\n%s", body)
	}
	if got := userMessage("Why.\n---\nProposed from dotvirt by u.\n"); got != "Why." {
		t.Errorf("message under an old footer = %q", got)
	}
}

// A draft that renders no title falls back to a count under the product name.
func TestFallbackTitleNamesTheProduct(t *testing.T) {
	r := &proposeRun{c: (&Coordinator{}).WithProductName("Uniwatt Cloud"), entries: make([]draft.Entry, 2)}
	if title, _ := r.draftText(); title != "Uniwatt Cloud: 2 change(s)" {
		t.Errorf("title = %q", title)
	}
}
