package changeset

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/drsgen"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// DRS (the descheduler tier) follows the platform-repo staging model: enabling
// or re-configuring stages the drsgen file set into the platform draft, and the
// committed state is read back off the base branch — the same
// stage → propose → merge → Argo-applies path as every other platform kind.

// StageEnableDRS makes (id, proj)'s draft represent exactly the base→spec
// delta — proj is the platform repo (DRS is cluster infrastructure, so it
// always routes to the platform tier). Declarative: the previous DRS entries
// are replaced wholesale (a re-configure never leaves a stale sibling like a
// dropped PSI opt-in behind), files already identical on the base branch are
// skipped, and a spec matching the base resolves to an empty delta — a clean
// draft, not an error. That empty case is also how a pending change is
// cancelled: re-submitting the committed configuration.
func (c *Coordinator) StageEnableDRS(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	var spec drsgen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid DRS spec: %v", model.ErrInvalid, err)
	}
	files, err := drsgen.Manifests(spec)
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if _, err := c.unstageResource(id, proj, draft.ResourceDRS); err != nil {
		return model.DraftView{}, err
	}
	for _, f := range files {
		if current, err := read.FileOnBranch(c.baseBranch, f.Path); err == nil && bytes.Equal(current, f.Content) {
			continue // already live in git; nothing to propose for this file
		}
		if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceDRS,
			Namespace:  ClusterScopeNS,
			Name:       f.Name,
			SourceFile: f.Path,
			Manifest:   string(f.Content),
		}); err != nil {
			return model.DraftView{}, err
		}
	}
	return c.Get(id, proj)
}

// StageDisableDRS stages the removal of the KubeDescheduler CR: rebalancing
// stops on merge, while the operator install (and any PSI MachineConfig) stays.
// When the CR was never committed, a pending enable in the draft is cleared
// instead — a cancel, not a delete.
func (c *Coordinator) StageDisableDRS(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	pending, err := c.unstageResource(id, proj, draft.ResourceDRS)
	if err != nil {
		return model.DraftView{}, err
	}
	if _, err := read.FileOnBranch(c.baseBranch, drsgen.CRPath); err != nil {
		if pending == 0 {
			return model.DraftView{}, fmt.Errorf("%w: DRS is not configured on %s", model.ErrNotFound, c.baseBranch)
		}
		return c.Get(id, proj) // cancelled the staged enable
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindDelete,
		Resource:   draft.ResourceDRS,
		Namespace:  ClusterScopeNS,
		Name:       "kubedescheduler",
		SourceFile: drsgen.CRPath,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// unstageResource drops every entry of one resource from (id, proj)'s draft,
// reporting how many it removed. Backs both the declarative DRS restage and
// the atomic-resource unstage (see draft.Resource.Atomic).
func (c *Coordinator) unstageResource(id auth.Identity, proj project.ProjectInfo, r draft.Resource) (int, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.Resource != r {
			continue
		}
		if err := c.store.Unstage(id.Username, proj.Name, e.Resource, e.Namespace, e.Name); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// DRSState reads the platform repo's committed DRS configuration off the base
// branch: whether the KubeDescheduler CR is there (and parses), and whether the
// PSI MachineConfig rode along. A missing file (or missing branch, e.g. a fresh
// platform repo) is "not configured", not an error.
func (c *Coordinator) DRSState(proj project.ProjectInfo) (model.DRSGitState, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DRSGitState{}, err
	}
	var out model.DRSGitState
	content, err := read.FileOnBranch(c.baseBranch, drsgen.CRPath)
	if err != nil {
		return out, nil
	}
	out.Configured = true
	// A hand-edited CR that no longer parses still reads as configured — the
	// panel then shows the raw state without a config form prefill.
	if spec, err := drsgen.Parse(content); err == nil {
		out.Config = configFromSpec(spec)
	}
	if _, err := read.FileOnBranch(c.baseBranch, drsgen.PSIPath); err == nil {
		out.PSIConfigured = true
	}
	return out, nil
}

// DRSDraft reads (id, proj)'s pending DRS entries back as configuration — the
// staged plane between committed and live. The panel seeds its dialog from
// this when present, so editing a not-yet-proposed change continues it (PSI
// opt-in included) instead of silently resetting to the committed state.
func (c *Coordinator) DRSDraft(id auth.Identity, proj project.ProjectInfo) (model.DRSDraftState, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.DRSDraftState{}, err
	}
	var out model.DRSDraftState
	for _, e := range entries {
		if e.Resource != draft.ResourceDRS {
			continue
		}
		switch {
		case e.Kind == draft.KindDelete:
			out.DisableStaged = true
		case e.Name == "kubedescheduler":
			if spec, err := drsgen.Parse([]byte(e.Manifest)); err == nil {
				out.Config = configFromSpec(spec)
			}
		case e.Name == "psi-machineconfig":
			out.PSI = true
		}
	}
	return out, nil
}

// configFromSpec resolves a parsed KubeDescheduler Spec into the config DTO.
func configFromSpec(spec drsgen.Spec) *model.DRSConfig {
	return &model.DRSConfig{
		Mode:               spec.Mode,
		Threshold:          spec.Threshold,
		IntervalSeconds:    spec.IntervalSeconds,
		SoftTainter:        spec.SoftTaint(),
		EvictionNodeLimit:  spec.EvictionNodeLimit,
		EvictionTotalLimit: spec.EvictionTotalLimit,
	}
}
