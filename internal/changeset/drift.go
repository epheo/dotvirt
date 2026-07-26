package changeset

import (
	"context"
	"fmt"
	"log"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The two reconcile directions for out-of-band cluster changes: Adopt proposes
// live->main (git catches up to the cluster), Resync forces main->live (the
// cluster catches up to git). VMDrift renders the gap between them.

// liveVM pairs a running object's parsed form with the manifest bytes git would hold,
// so one read serves both the comparison and the adoption.
type liveVM struct {
	vm      model.VM
	content []byte
}

// liveVMs is the "actual" side of every reconcile: the cluster itself, serialized into
// the shape git holds. Parsed with the same parser that reads the repo, so a diff
// compares like with like and never reports the serializer as drift. Callers pass only
// the namespaces they are asking about; a one-VM lookup must not marshal the project.
//
// Refuses while the VM reflector is still on its initial LIST: absent-from-live is what
// makes VMDrift report drift and Adopt say the VM is not running, so a half-filled
// snapshot would flag every tracked VM in the project. Unavailable beats wrong.
func (c *Coordinator) liveVMs(namespaces []string) ([]liveVM, error) {
	if c.live == nil {
		return nil, fmt.Errorf("%w: live cluster state", model.ErrUnavailable)
	}
	if !c.live.Ready() {
		return nil, fmt.Errorf("%w: live cluster state still loading", model.ErrUnavailable)
	}
	var out []liveVM
	for _, m := range c.live.VMManifests(namespaces) {
		// An exported manifest always carries its namespace, so there is none to default.
		vms, err := manifest.ParseVMs(m.Path, m.Content, "")
		if err != nil {
			// One unreadable VM must not take the project's whole drift view with it; the
			// adapter that serialized it drops its own failures the same way.
			log.Printf("live manifest %s: %v", m.Path, err)
			continue
		}
		for _, vm := range vms {
			out = append(out, liveVM{vm: vm, content: m.Content})
		}
	}
	return out, nil
}

func (c *Coordinator) findLiveVM(namespace, name string) (liveVM, bool, error) {
	live, err := c.liveVMs([]string{namespace})
	if err != nil {
		return liveVM{}, false, err
	}
	for _, l := range live {
		if l.vm.Namespace == namespace && l.vm.Name == name {
			return l, true, nil
		}
	}
	return liveVM{}, false, nil
}

// Adopt stages a VM's live state into (id, proj)'s draft, so out-of-band cluster
// changes can be proposed INTO main (live->main reconcile): as an edit making base
// match the cluster when the VM is tracked, or as a create of its manifest when it
// exists only in the cluster (a fresh clone target, an out-of-band create).
func (c *Coordinator) Adopt(id auth.Identity, proj project.ProjectInfo, namespace, name string) (model.DraftView, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	desired, okD, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	actual, okA, err := c.findLiveVM(namespace, name)
	if err != nil {
		return model.DraftView{}, err
	}
	if !okA {
		return model.DraftView{}, fmt.Errorf("%w: %s/%s is not running", model.ErrNotFound, namespace, name)
	}
	if !okD {
		if err := c.stageAdoptCreate(id.Username, proj.Name, actual); err != nil {
			return model.DraftView{}, err
		}
		return c.Get(id, proj)
	}

	edit := editToMatch(desired, actual.vm)
	if edit.Empty() {
		return model.DraftView{}, fmt.Errorf("%w: no drift to adopt for %s/%s", model.ErrInvalid, namespace, name)
	}
	if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
		Kind:      draft.KindEdit,
		Namespace: namespace,
		Name:      name,
		// The file main actually holds, which need not be where an export would put it.
		SourceFile: desired.SourceFile,
		Edit:       &edit,
	}); err != nil {
		return model.DraftView{}, err
	}
	return c.Get(id, proj)
}

// stageAdoptCreate stages a brand-new manifest from live state, for a VM with no file
// on base. The serialized bytes are staged verbatim, so the proposal is exactly what
// the cluster holds. It only stages, so AdoptNamespace can loop it before one Get.
func (c *Coordinator) stageAdoptCreate(username, projName string, l liveVM) error {
	return c.store.Stage(username, projName, draft.Entry{
		Kind:       draft.KindCreate,
		Namespace:  l.vm.Namespace,
		Name:       l.vm.Name,
		SourceFile: l.vm.SourceFile,
		Manifest:   string(l.content),
	})
}

// Adoptable is one object the caller captured from the cluster, ready to stage.
type Adoptable struct {
	Namespace string
	Name      string
	Kind      string
	Path      string
	Manifest  []byte
}

// AdoptNamespace stages everything the namespace runs that git does not describe, as
// one draft: the whole namespace comes under GitOps in a single PR, not just its VMs.
// The caller captures under its own token (cluster.AdoptableObjects) and this only
// stages, keeping the coordinator cluster-free. Idempotent: re-staging replaces the
// draft entry.
//
// What base already declares is dropped here rather than by the capture, because git
// is the authority on that and only the coordinator can read it. Skipping it would
// restate the repo, overwriting hand-authored manifests with the live defaulted copy.
func (c *Coordinator) AdoptNamespace(id auth.Identity, proj project.ProjectInfo, namespace string, objs []Adoptable) (model.DraftView, error) {
	if err := requireRepo(proj); err != nil {
		return model.DraftView{}, err
	}
	read, err := c.read(proj)
	if err != nil {
		return model.DraftView{}, err
	}
	declared, err := read.DeclaredOnBranch(c.baseBranch)
	if err != nil {
		return model.DraftView{}, err
	}
	staged := 0
	for _, o := range objs {
		if declared[model.ObjectRef{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}] {
			continue
		}
		if err := c.store.Stage(id.Username, proj.Name, draft.Entry{
			Kind:       draft.KindCreate,
			Resource:   adoptResource(o.Kind),
			Namespace:  o.Namespace,
			Name:       o.Name,
			SourceFile: o.Path,
			Manifest:   string(o.Manifest),
		}); err != nil {
			return model.DraftView{}, err
		}
		staged++
	}
	if staged == 0 {
		return model.DraftView{}, fmt.Errorf("%w: nothing to adopt in %s: git already describes everything running there", model.ErrInvalid, namespace)
	}
	return c.Get(id, proj)
}

// adoptResource maps a captured kind onto the draft vocabulary, so the Changes view
// labels an adopted network as a network rather than as a VM (the empty default). A kind
// with no term stays ResourceVM rather than minting one: the vocabulary is a closed set
// the draft store and the Changes view both switch on, and an unknown value would render
// and unstage as nothing.
func adoptResource(kind string) draft.Resource {
	switch kind {
	case "UserDefinedNetwork":
		return draft.ResourceNetwork
	case "EgressFirewall":
		return draft.ResourceEgressFirewall
	case "NetworkPolicy":
		return draft.ResourceNetworkPolicy
	default:
		return draft.ResourceVM
	}
}

// Resync triggers an ArgoCD sync of the Application managing the VM, bringing the
// cluster back to git (main->live reconcile). Writes nothing to git. It uses the
// SA-identity resyncer (Argo operations have no user context, but the request's
// ctx still bounds the call so a hung Argo op doesn't outlive the HTTP request).
// Because this is the one operation that escalates to dotvirt's SA, the caller's
// own authority over the VM (canUpdateVM, a user-token SSAR) is enforced here,
// beside the escalation — not only at the transport layer.
func (c *Coordinator) Resync(ctx context.Context, canUpdateVM func(context.Context, string, string) (bool, error), namespace, name string) (model.ResyncResult, error) {
	if c.resyncer == nil {
		return model.ResyncResult{}, fmt.Errorf("%w: re-sync unavailable (ArgoCD not configured)", model.ErrUnavailable)
	}
	if allowed, err := canUpdateVM(ctx, namespace, name); err != nil {
		return model.ResyncResult{}, err
	} else if !allowed {
		return model.ResyncResult{}, fmt.Errorf("%w: you don't have permission to sync VM %s/%s", model.ErrForbidden, namespace, name)
	}
	return c.resyncer.Resync(ctx, namespace, name)
}

// VMDrift returns the semantic diff between a VM as it runs (actual) and as main
// declares it (desired), within proj's repo.
func (c *Coordinator) VMDrift(proj project.ProjectInfo, namespace, name string) (model.DriftResult, error) {
	read, err := c.read(proj)
	if err != nil {
		return model.DriftResult{}, err
	}
	desired, okD, err := read.FindVMOnBranch(c.baseBranch, namespace, name)
	if err != nil {
		return model.DriftResult{}, err
	}
	actual, okA, err := c.findLiveVM(namespace, name)
	if err != nil {
		return model.DriftResult{}, err
	}
	result := model.DriftResult{}
	if !okD || !okA {
		result.Drift = okD != okA
		return result, nil
	}
	result.Changes = manifest.DiffVMs(desired, actual.vm)
	result.Drift = len(result.Changes) > 0
	return result, nil
}
