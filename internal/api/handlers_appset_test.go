package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/project"
)

// The ApplicationSet plugin endpoint is pre-session (isOpenPath): its shared
// bearer is the ONLY thing between the internet-facing route and the full
// project->repo map, so the auth corner cases deserve pinning.
func TestAppSetPluginAuth(t *testing.T) {
	newServer := func(token string) *Server {
		bus := eventbus.New()
		sa := cluster.NewClient(fake.NewSimpleClientset(), nil, nil)
		return NewServer(Deps{
			State:    clusterstate.New(sa, "dotvirt.io/project", bus),
			Bus:      bus,
			Resolver: project.NewResolver("dotvirt.io/project", "dotvirt.io/repo", ""),
			Config:   Config{AppSetPluginToken: token},
		})
	}
	post := func(s *Server, authz string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		s.handleAppSetPlugin(rec, r)
		return rec
	}

	t.Run("unconfigured plugin is absent, not open", func(t *testing.T) {
		if rec := post(newServer(""), "Bearer anything"); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("wrong or missing bearer is unauthorized", func(t *testing.T) {
		s := newServer("the-shared-token")
		for _, authz := range []string{"", "Bearer wrong", "Bearer the-shared-token-and-more", "the-shared-token"} {
			if rec := post(s, authz); rec.Code != http.StatusUnauthorized {
				t.Errorf("authz %q = %d, want 401", authz, rec.Code)
			}
		}
	})

	t.Run("correct bearer gets the generator envelope", func(t *testing.T) {
		rec := post(newServer("the-shared-token"), "Bearer the-shared-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %q, want 200", rec.Code, rec.Body.String())
		}
		var out struct {
			Output struct {
				Parameters []map[string]string `json:"parameters"`
			} `json:"output"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// Empty snapshot -> an empty parameter list, present, not null: the
		// ApplicationSet controller chokes on a missing field.
		if out.Output.Parameters == nil {
			t.Fatal("parameters must be an empty list, not absent")
		}
	})
}
