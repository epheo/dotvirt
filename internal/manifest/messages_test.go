package manifest

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// The rebalancing opt-out reaches the Changes pane and a refused edit's text
// under the UI's word for it.
func TestRebalancingSpeaksTheUIsWords(t *testing.T) {
	excluded := ptr(true)
	for _, cs := range [][]model.Change{
		ChangesForEdit(model.VM{}, VMEdit{DRSExclude: excluded}),
		DiffVMs(model.VM{}, model.VM{DRSExclude: true}),
	} {
		if len(cs) != 1 || cs[0].Field != "Rebalancing" {
			t.Errorf("changes = %+v, want one Rebalancing row", cs)
		}
	}
	err := verifyEdit([]byte(guardVM), "alpha", "alpha", "web", VMEdit{DRSExclude: excluded})
	if !errors.Is(err, model.ErrConflict) || !strings.Contains(err.Error(), "rebalancing exclusion") || strings.Contains(err.Error(), "DRS") {
		t.Errorf("unapplied opt-out: %v, want it named as the rebalancing exclusion", err)
	}
}
