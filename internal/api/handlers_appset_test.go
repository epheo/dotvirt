package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			Reader:   &fakeDraft{},
			Draft:    &fakeDraft{},
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

// Each repo-backed project gets a destination its AppProject admits, and it
// holds still as namespaces join: the namespace named after the project, else
// the oldest, never platform space (the operator's reserved list included). A
// project no AppProject can confine gets no Application.
func TestAppSetPluginParams(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	label := func(name, proj string, age time.Duration) corev1.Namespace {
		return corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(t0.Add(age)),
			Labels: map[string]string{"dotvirt.io/project": proj}, Annotations: map[string]string{"dotvirt.io/repo": "https://forge/" + proj + ".git"}}}
	}
	base := []corev1.Namespace{
		label("team-b-prod", "team-b", 0), label("team-b-dev", "team-b", time.Hour),
		label("aaa", "team-c", 0), label("team-c", "team-c", time.Hour),
		label("kube-k", "team-k", -time.Hour), label("k-argo", "team-k", -time.Hour), label("k-work", "team-k", time.Hour),
		label("r-argo", "team-r", 0),
		label("bad", "Team_Bad", 0),
	}
	destinations := func(nss []corev1.Namespace) map[string]string {
		fc := newFakeCluster(t)
		fc.namespaces = append(fc.namespaces, nss...)
		s := fc.server(nil, Config{AppSetPluginToken: "tok"})
		body := `{"applicationSetName":"dotvirt-projects","input":{"parameters":{"reservedNamespaces":["k-argo","r-argo"]}}}`
		r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		s.handleAppSetPlugin(rec, r)
		var out struct {
			Output struct {
				Parameters []map[string]string `json:"parameters"`
			} `json:"output"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]string{}
		for _, p := range out.Output.Parameters {
			got[p["project"]] = p["namespace"]
		}
		return got
	}
	want := map[string]string{"team-a": "team-a-dev", "team-b": "team-b-prod", "team-c": "team-c", "team-k": "k-work"}
	if got := destinations(base); !maps.Equal(got, want) {
		t.Fatalf("destinations = %v, want %v", got, want)
	}
	joined := append(slices.Clone(base), label("team-b-aaa", "team-b", 2*time.Hour), label("c-new", "team-c", 2*time.Hour))
	if got := destinations(joined); !maps.Equal(got, want) {
		t.Errorf("after namespaces joined: destinations = %v, want %v", got, want)
	}
}

// The ApplicationSet renders with missingkey=error: every project needs a path.
func TestAppSetPluginPath(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "in-folder",
		Labels:      map[string]string{"dotvirt.io/project": "folded"},
		Annotations: map[string]string{"dotvirt.io/repo": "https://forge/folded.git", "dotvirt.io/path": "clusters/a"}}})
	s := fc.server(nil, Config{AppSetPluginToken: "tok"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/getparams.execute", strings.NewReader(`{"input":{"parameters":{"reservedNamespaces":[]}}}`))
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.handleAppSetPlugin(rec, r)
	var out struct {
		Output struct {
			Parameters []map[string]string `json:"parameters"`
		} `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range out.Output.Parameters {
		got[p["project"]] = p["path"]
	}
	if got["folded"] != "clusters/a" || got["team-a"] != "." {
		t.Errorf("paths = %v", got)
	}
}
