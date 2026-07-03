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

// StageEnableDRS records the DRS (descheduler) file set in (id, proj)'s draft —
// proj is the platform repo (DRS is cluster infrastructure, so it always routes
// to the platform tier). Files already identical on the base branch are skipped,
// so a first enable stages the whole operator install while a re-configure
// stages only the KubeDescheduler CR that changed; a spec that changes nothing
// is rejected rather than staging an empty set.
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
	// Restage from scratch so a re-configure (e.g. dropping InstallPSI) never
	// leaves a stale sibling entry behind.
	if err := c.unstageDRS(id, proj); err != nil {
		return model.DraftView{}, err
	}
	staged := 0
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
		staged++
	}
	if staged == 0 {
		return model.DraftView{}, fmt.Errorf("%w: this DRS configuration is already on %s", model.ErrInvalid, c.baseBranch)
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
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.DraftView{}, err
	}
	pending := 0
	for _, e := range entries {
		if e.Resource == draft.ResourceDRS {
			pending++
		}
	}
	if err := c.unstageDRS(id, proj); err != nil {
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

// unstageDRS drops every DRS entry from (id, proj)'s draft.
func (c *Coordinator) unstageDRS(id auth.Identity, proj project.ProjectInfo) error {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Resource != draft.ResourceDRS {
			continue
		}
		if err := c.store.Unstage(id.Username, proj.Name, e.Resource, e.Namespace, e.Name); err != nil {
			return err
		}
	}
	return nil
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
		soft := true
		if spec.SoftTainter != nil {
			soft = *spec.SoftTainter
		}
		out.Config = &model.DRSConfig{
			Mode:               spec.Mode,
			Threshold:          spec.Threshold,
			IntervalSeconds:    spec.IntervalSeconds,
			SoftTainter:        soft,
			EvictionNodeLimit:  spec.EvictionNodeLimit,
			EvictionTotalLimit: spec.EvictionTotalLimit,
		}
	}
	if _, err := read.FileOnBranch(c.baseBranch, drsgen.PSIPath); err == nil {
		out.PSIConfigured = true
	}
	return out, nil
}
