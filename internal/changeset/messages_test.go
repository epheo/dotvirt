package changeset

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// A refusal's text reaches the user verbatim, so it names things in the UI's
// words: never a persisted resource name, never the platform's internals.
func TestRefusalsSpeakTheUIsWords(t *testing.T) {
	bare := seedBareFiles(t, nil)
	c := newTestCoordinator(t, false)
	id := auth.Identity{Username: "admin"}
	proj := project.ProjectInfo{Name: "platform", Repo: bare}

	_, notConfigured := c.StageDisableDRS(id, proj)
	_, badSpec := c.StageEnableDRS(id, proj, json.RawMessage(`{"mode":7}`))
	_, noForm := createForm(draft.ResourceDRS, nil)
	// want is the whole message; a decoder's own words follow the colon of
	// the one case that wraps them.
	cases := []struct {
		err  error
		kind error
		want string
	}{
		{notConfigured, model.ErrNotFound, "not found: rebalancing is not configured on main"},
		{badSpec, model.ErrInvalid, "invalid request: invalid rebalancing spec: "},
		{tenantName(project.PlatformName), model.ErrInvalid, `invalid request: "platform" is reserved for the platform repo`},
		{noForm, model.ErrInvalid, "invalid request: there is no create form for rebalancing"},
		{tenantDeclarable(project.ProjectInfo{Name: "p"}, "NetworkAttachmentDefinition", "raw"), model.ErrInvalid,
			"invalid request: only the platform repo may declare NetworkAttachmentDefinition raw, not project p"},
	}
	for _, tc := range cases {
		got := fmt.Sprint(tc.err)
		if strings.HasSuffix(tc.want, ": ") {
			got = got[:min(len(got), len(tc.want))]
		}
		if !errors.Is(tc.err, tc.kind) || got != tc.want {
			t.Errorf("err = %v, want %q", tc.err, tc.want)
		}
	}
}
