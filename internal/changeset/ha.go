package changeset

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/hagen"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// HA (the node-remediation tier) follows the platform-repo staging model:
// enabling or re-configuring stages the hagen file set into the platform
// draft, and the committed state is read back off the base branch - the same
// stage -> propose -> merge -> Argo-applies path as DRS.

// StageEnableHA makes (id, proj)'s draft represent exactly the base->spec
// delta - proj is the platform repo (fencing is cluster infrastructure, so it
// always routes to the platform tier). Declarative: the previous HA entries
// are replaced wholesale, files already identical on the base branch are
// skipped, and a spec matching the base resolves to an empty delta - a clean
// draft, not an error. That empty case is also how a pending change is
// cancelled: re-submitting the committed configuration.
func (c *Coordinator) StageEnableHA(id auth.Identity, proj project.ProjectInfo, rawSpec json.RawMessage) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	var spec hagen.Spec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		return model.DraftView{}, fmt.Errorf("%w: invalid HA spec: %v", model.ErrInvalid, err)
	}
	files, err := hagen.Manifests(spec)
	if err != nil {
		return model.DraftView{}, fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	if _, err := c.unstageResource(id, proj, draft.ResourceHA); err != nil {
		return model.DraftView{}, err
	}
	for _, f := range files {
		if current, err := read.FileOnBranch(c.baseBranch, f.Path); err == nil && bytes.Equal(current, f.Content) {
			continue // already live in git; nothing to propose for this file
		}
		if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   draft.ResourceHA,
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

// StageDisableHA stages the removal of the NodeHealthCheck CR: fencing stops
// on merge, while the operator install stays. When the CR was never
// committed, a pending enable in the draft is cleared instead - a cancel, not
// a delete.
func (c *Coordinator) StageDisableHA(id auth.Identity, proj project.ProjectInfo) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	pending, err := c.unstageResource(id, proj, draft.ResourceHA)
	if err != nil {
		return model.DraftView{}, err
	}
	if _, err := read.FileOnBranch(c.baseBranch, hagen.CRPath); err != nil {
		if pending == 0 {
			return model.DraftView{}, fmt.Errorf("%w: HA is not configured on %s", model.ErrNotFound, c.baseBranch)
		}
		return c.Get(id, proj) // cancelled the staged enable
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:       draft.KindDelete,
		Resource:   draft.ResourceHA,
		Namespace:  ClusterScopeNS,
		Name:       "nodehealthcheck",
		SourceFile: hagen.CRPath,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// HAState reads the platform repo's committed HA configuration off the base
// branch: whether the NodeHealthCheck CR is there (and parses). A missing file
// (or missing branch, e.g. a fresh platform repo) is "not configured", not an
// error.
func (c *Coordinator) HAState(proj project.ProjectInfo) (model.HAGitState, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.HAGitState{}, err
	}
	var out model.HAGitState
	content, err := read.FileOnBranch(c.baseBranch, hagen.CRPath)
	if err != nil {
		return out, nil
	}
	out.Configured = true
	// A hand-edited CR that no longer parses still reads as configured - the
	// panel then shows the raw state without a config form prefill.
	if spec, err := hagen.Parse(content); err == nil && spec != (hagen.Spec{}) {
		out.Config = haConfigFromSpec(spec)
	}
	return out, nil
}

// HADraft reads (id, proj)'s pending HA entries back as configuration - the
// staged plane between committed and live. The panel seeds its dialog from
// this when present, so editing a not-yet-proposed change continues it
// instead of silently resetting to the committed state.
func (c *Coordinator) HADraft(id auth.Identity, proj project.ProjectInfo) (model.HADraftState, error) {
	entries, err := c.store.List(id.Username, proj.Name)
	if err != nil {
		return model.HADraftState{}, err
	}
	var out model.HADraftState
	for _, e := range entries {
		if e.Resource != draft.ResourceHA {
			continue
		}
		switch {
		case e.Kind == draft.KindDelete:
			out.DisableStaged = true
		case e.Name == "nodehealthcheck":
			if spec, err := hagen.Parse([]byte(e.Manifest)); err == nil {
				out.Config = haConfigFromSpec(spec)
			}
		}
	}
	return out, nil
}

// haConfigFromSpec resolves a parsed NodeHealthCheck Spec into the config DTO.
func haConfigFromSpec(spec hagen.Spec) *model.HAConfig {
	return &model.HAConfig{
		UnhealthySeconds:  spec.UnhealthySeconds,
		MinHealthyPercent: spec.MinHealthyPercent,
	}
}
