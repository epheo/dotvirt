package changeset

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func TestDefaultTitle(t *testing.T) {
	item := func(kind, name string, changes ...model.Change) model.DraftItem {
		return model.DraftItem{Kind: kind, Namespace: "demo", Name: name, Changes: changes}
	}
	cases := []struct {
		name  string
		items []model.DraftItem
		want  string
	}{
		{"empty", nil, ""},
		{"power", []model.DraftItem{item("edit", "web", model.Change{Field: "Power", Action: "change", From: "Off", To: "On"})},
			"web: power on"},
		{"sizing values", []model.DraftItem{item("edit", "web",
			model.Change{Field: "CPU", Action: "change", From: "2 vCPU", To: "4 vCPU", Restart: true},
			model.Change{Field: "Memory", Action: "change", From: "4Gi", To: "8Gi"})},
			"web: 4 vCPU, 8 GiB memory"},
		{"one field, not a value", []model.DraftItem{item("edit", "web", model.Change{Field: "Label env", Action: "add", To: "prod"})},
			"Update web: Label env + prod"},
		{"not a VM", []model.DraftItem{{Kind: "edit", Resource: "network", Namespace: "demo", Name: "lan",
			Changes: []model.Change{{Field: "Power", Action: "change", From: "Off", To: "On"}}}},
			"Update lan: Power Off -> On"},
		{"many fields", []model.DraftItem{item("edit", "web",
			model.Change{Field: "CPU", Action: "change", From: "2 vCPU", To: "4 vCPU"},
			model.Change{Field: "Memory", Action: "change", From: "2Gi", To: "4Gi"},
			model.Change{Field: "Disk", Action: "add", To: "data (50Gi)"},
			model.Change{Field: "Disk", Action: "add", To: "logs (10Gi)"},
			model.Change{Field: "Label env", Action: "add", To: "prod"})},
			"Update web: CPU, Memory, Disk (+1 more)"},
		{"same kind", []model.DraftItem{item("delete", "web"), item("delete", "db")}, "Delete web, db"},
		{"create", []model.DraftItem{item("create", "cache", model.Change{Field: "Create VM", Action: "add", To: "demo/cache"})}, "Create cache"},
		{"mixed", []model.DraftItem{item("edit", "web"), item("create", "cache"), item("delete", "old"), item("delete", "older")},
			"4 changes: web, cache, old (+1 more)"},
	}
	for _, c := range cases {
		if got := defaultTitle(model.DraftView{Items: c.items}); got != c.want {
			t.Errorf("%s: defaultTitle = %q, want %q", c.name, got, c.want)
		}
	}
}
