package changeset

import (
	"strings"
	"testing"

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

	body := prBody(view, "resize the web tier", "kube:admin")
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
	body := prBody(view, "", "u")
	if strings.Contains(body, "power cycle") {
		t.Errorf("restart note rendered without any restart-flagged change:\n%s", body)
	}
}
