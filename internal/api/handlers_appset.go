package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/pkg/tenancy"
)

// handleAppSetPlugin serves the ArgoCD ApplicationSet plugin generator: it returns
// one {project,repo,path,namespace} parameter per repo-backed project (1 app = 1 project =
// its N labeled namespaces), derived from the dotvirt.io/project labels (read from the
// SA snapshot). Every emitted app lands in its project's own AppProject, which the
// operator derives from the same labels and confines to those namespaces and that
// repo under the same pkg/tenancy rules, so this list can only ever mint RESTRICTED
// tenant apps (namespaced workloads, no cluster-scoped infra, no other tenant's
// namespace) - the privileged platform app
// is static and platform-owned, never generated from here. Labeling a namespace
// folds it into its project's existing app - dotvirt supplies the list but never
// creates the App, so "owns nothing" holds. Authenticated by a shared token (not a
// user session); the path is exempted from user-auth in auth.isOpenPath.
func (s *Server) handleAppSetPlugin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AppSetPluginToken == "" {
		fail(w, fmt.Errorf("%w: appset plugin not configured", model.ErrNotFound))
		return
	}
	// Require the Bearer scheme outright: TrimPrefix alone would also accept a
	// bare "Authorization: <token>", quietly widening the accepted forms.
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	got := strings.TrimPrefix(authz, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AppSetPluginToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The operator names the platform namespaces in the generator's input, so the
	// destination is chosen against the same reserved set its AppProjects use.
	req, ok := decodeOptional[pluginRequest](w, r)
	if !ok {
		return
	}
	platform := req.Input.Parameters.ReservedNamespaces
	type param struct {
		Project   string `json:"project"`
		Repo      string `json:"repo"`
		Path      string `json:"path"`
		Namespace string `json:"namespace"`
		// AllowEmpty is the app's automated.allowEmpty (the operator's
		// templatePatch): on only while a deliberate removal of the project's
		// last objects is held, so Argo's guard against an emptied source
		// still holds a repo that was lost and re-created.
		AllowEmpty bool `json:"allowEmpty"`
	}
	all := s.state.Namespaces()
	created := make(map[string]time.Time, len(all))
	for _, ns := range all {
		created[ns.Name] = ns.Created
	}
	params := []param{}
	for _, p := range s.resolver.Resolve(all, nil) {
		if p.Repo == "" {
			continue // a project with no usable repo can't be synced
		}
		// No AppProject can confine it, or none of its namespaces is admitted:
		// an Application would fail, so none is generated.
		if _, ok := tenancy.AppProjectName(p.Name); !ok {
			continue
		}
		nss := make([]tenancy.Namespace, 0, len(p.Namespaces))
		for _, ns := range p.Namespaces {
			nss = append(nss, tenancy.Namespace{Name: ns, Created: created[ns]})
		}
		dest, ok := tenancy.Destination(p.Name, nss, platform)
		if !ok {
			continue
		}
		root := p.Path
		if root == "" {
			root = "."
		}
		params = append(params, param{Project: p.Name, Repo: p.Repo, Path: root, Namespace: dest, AllowEmpty: s.liftsAllowEmpty(p)})
	}
	writeJSON(w, map[string]any{"output": map[string]any{"parameters": params}})
}

// EmptyHold is what it means that ArgoCD holds p's generated app at an empty
// source (argo.Snapshot.EmptyHold), "" while it does not; changeset.HoldReader.
// Deleting needs git itself to have emptied by moving on from what Argo
// applied, read from the source Argo applied: the held revision descends from
// the applied one in dotvirt's mirror, differs from it, and declares nothing
// in the project's folder. An empty render alone (a re-pointed path, an Argo
// that reads the source otherwise) is a mismatch and never lifts the guard.
// A held project's mirror is kept open (RepoSet.Warm): the merge that emptied
// it may land long after anyone last opened the repo.
func (s *Server) EmptyHold(p project.ProjectInfo) model.Hold {
	if s.drift == nil || p.Repo == "" {
		return ""
	}
	h, ok := s.drift.EmptyHold(p.Name)
	if !ok {
		return ""
	}
	if !h.Reads(p.Repo, p.Path) {
		return model.HoldPending
	}
	read, ok := s.repos.Warm(p.Repo, p.Path)
	if !ok {
		return model.HoldPending
	}
	f, ok := s.holdFacts.of(read, s.cfg.BaseBranch, h)
	switch {
	case !ok:
		return model.HoldPending
	case !f.descends:
		return model.HoldReplaced
	case h.Compared == h.Applied || !h.SameSource || f.declares:
		return model.HoldMismatch
	}
	return model.HoldDeleting
}

// liftsAllowEmpty is the generator's allowEmpty for p: a deliberate removal,
// read off a live Application snapshot (a stale one may hold a hold that is
// gone). Once lifted, only the next generator run closes it again: Argo
// applies the removal within seconds, so that window is one requeue.
func (s *Server) liftsAllowEmpty(p project.ProjectInfo) bool {
	return s.drift != nil && s.drift.Healthy() && s.EmptyHold(p) == model.HoldDeleting
}

// holdMemo keeps what git says of a held app's two revisions, which never
// changes once known: whether the held one descends from the applied one, and
// whether it declares anything. Every frame and generator run asks per held
// project; an unknown answer (a mirror behind) is asked again.
type holdMemo struct {
	mu sync.Mutex
	m  map[string]holdFact
}

type holdFact struct{ descends, declares bool }

// holdMemoMax bounds the memo; holds are rare and short-lived.
const holdMemoMax = 256

func (m *holdMemo) of(read *git.Repo, branch string, h argo.EmptyHold) (holdFact, bool) {
	key := h.Repo + "\x00" + h.Path + "\x00" + h.Applied + "\x00" + h.Compared
	m.mu.Lock()
	f, ok := m.m[key]
	m.mu.Unlock()
	if ok {
		return f, true
	}
	descends, known, err := read.Descends(branch, h.Compared, h.Applied)
	if err != nil || !known {
		return holdFact{}, false
	}
	f.descends = descends
	if descends {
		if f.declares, err = read.DeclaresAt(h.Compared); err != nil {
			return holdFact{}, false
		}
	}
	m.mu.Lock()
	if m.m == nil || len(m.m) >= holdMemoMax {
		m.m = map[string]holdFact{}
	}
	m.m[key] = f
	m.mu.Unlock()
	return f, true
}

// pluginRequest is the plugin generator's request: the ApplicationSet's
// generator input, as the operator renders it.
type pluginRequest struct {
	Input struct {
		Parameters struct {
			ReservedNamespaces []string `json:"reservedNamespaces"`
		} `json:"parameters"`
	} `json:"input"`
}
