package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// The tenant authoring gate. Namespace membership (resolveProject) only proves
// the caller can read VMs there; a draft write needs the RBAC the change would
// need if the caller applied it themselves, because the PR opens under
// dotvirt's forge identity and Argo applies it with its own. The platform tier
// keeps its own create-SSAR gates (platformScope), so these pass it through.

const (
	verbList   = "list"
	verbCreate = "create"
	verbUpdate = "update"
	verbDelete = "delete"
)

var ssarVM = ssarFor(model.KindVM)

// stagedKinds is what a change to resource ns/name stages: its namespaced
// backing, or for a network (UDN or a raw NAD) the backing the catalog shows
// for that name. An unknown network checks every namespaced backing.
func (s *Server) stagedKinds(resource, ns, name string) []model.Kind {
	res, ok := model.LookupResource(resource)
	if !ok {
		return nil
	}
	var kinds []model.Kind
	for _, k := range res.Kinds {
		if !k.ClusterScoped {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) > 1 && s.netstate != nil {
		for _, n := range s.netstate.Catalog().Networks {
			if n.Namespace == ns && n.Name == name {
				for _, k := range kinds {
					if k.Kind == n.Backing {
						return []model.Kind{k}
					}
				}
			}
		}
	}
	return kinds
}

// authorize gates one staged change: verb on ref in ns. ok=false means the
// response is written.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, sc scope, verb string, ref ssarRef, ns string) bool {
	if sc.platform {
		return true
	}
	ok, err := s.can(r.Context(), sc.id, sc.cluster, verb, ref, ns)
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: you may not %s %s in %s; staging changes needs that right, read access is not enough",
			model.ErrForbidden, verb, ref.resource, ns))
		return false
	}
	return true
}

// authorizeObject gates a change to a network-family object by its draft
// identity; a cluster-scoped one already passed platformScope.
func (s *Server) authorizeObject(w http.ResponseWriter, r *http.Request, sc scope, verb, resource, ns, name string) bool {
	if ns == model.ClusterScopeNS {
		return true
	}
	kinds := s.stagedKinds(resource, ns, name)
	if len(kinds) == 0 {
		fail(w, invalid(fmt.Errorf("%s is not namespace-scoped", resource)))
		return false
	}
	for _, k := range kinds {
		if !s.authorize(w, r, sc, verb, ssarFor(k), ns) {
			return false
		}
	}
	return true
}

// authorizeDraft gates the routes that open a PR from the caller's draft or
// write a project library: authoring rights in any of the project's
// namespaces. Drafts are per user and every entry passed its own per-change
// gate, so a mixed-role caller may still propose what they could stage.
// Discard and unstage stay ungated: they only clear the caller's own local
// draft, so a demoted user can drop leftovers.
func (s *Server) authorizeDraft(w http.ResponseWriter, r *http.Request, sc scope) bool {
	if sc.platform {
		return true
	}
	for _, ns := range sc.proj.Namespaces {
		ok, err := s.can(r.Context(), sc.id, sc.cluster, verbUpdate, ssarVM, ns)
		if err != nil {
			fail(w, s.reviewFailed(r.Context(), sc.id, err))
			return false
		}
		if ok {
			return true
		}
	}
	fail(w, fmt.Errorf("%w: you may not update virtualmachines in any namespace of %s; proposing changes needs edit rights",
		model.ErrForbidden, sc.proj.Name))
	return false
}

// authorizeRepo gates a revert before the coordinator weighs it: authoring
// rights in every namespace of a tenant project, the ones hidden from the
// caller included, since a commit may touch any of them. The coordinator then
// checks each object the revert changes with its exact verb, which is all the
// platform tier's revert passes: its repo has no namespaces of its own.
func (s *Server) authorizeRepo(w http.ResponseWriter, r *http.Request, sc scope) bool {
	// A namespace the caller cannot read is one they cannot author; it is
	// refused without being named.
	if full, ok := s.projectByName(sc.proj.Name); ok && len(full.Namespaces) > len(sc.proj.Namespaces) {
		fail(w, fmt.Errorf("%w: reverting needs edit rights in every namespace of %s", model.ErrForbidden, sc.proj.Name))
		return false
	}
	for _, ns := range sc.proj.Namespaces {
		if !s.authorize(w, r, sc, verbUpdate, ssarVM, ns) {
			return false
		}
	}
	return true
}

// authorConcurrency bounds the per-namespace reviews one frame fans out.
const authorConcurrency = 8

// authorNamespaces is the UI's authoring signal for nss. One cluster-wide
// review answers for every namespace when it allows; otherwise each namespace
// is reviewed, at most authorConcurrency at once. A namespace whose review
// fails is left out of the map, so a timeout reads as unknown, never as no.
func (s *Server) authorNamespaces(ctx context.Context, id auth.Identity, c *cluster.Client, nss []string) map[string]bool {
	out := make(map[string]bool, len(nss))
	if all, err := s.can(ctx, id, c, verbUpdate, ssarVM, ""); err == nil && all {
		for _, ns := range nss {
			out[ns] = true
		}
		return out
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, authorConcurrency)
	)
	for _, ns := range nss {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ok, err := s.can(ctx, id, c, verbUpdate, ssarVM, ns)
			if err != nil {
				return
			}
			mu.Lock()
			out[ns] = ok
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// authorizeTimeout bounds the reviews one gated change asks for: the
// coordinator calls without the request's context.
const authorizeTimeout = 15 * time.Second

// Authorize implements changeset.Authorizer: every access a staged, proposed
// or reverted change makes Argo perform, asked under id's own token through
// the cached reviews, at most authorConcurrency at once.
//
// A create may land on an object that already runs, which Argo then patches:
// an update. One the SA snapshot holds is asked as that update; one it knows
// absent (a VM, once its snapshot synced) as the create; any other is
// dry-run (dryRunCreate). A create the change pairs with a delete is a
// replace and stays a create.
func (s *Server) Authorize(id auth.Identity, need []changeset.Access) error {
	if s.clusterF == nil {
		return unavailable(errors.New("cluster not configured"))
	}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		return unavailable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), authorizeTimeout)
	defer cancel()
	identity := func(a changeset.Access) changeset.Access {
		return changeset.Access{Group: a.Group, Resource: a.Resource, Namespace: a.Namespace, Name: a.Name}
	}
	deleted := map[changeset.Access]bool{}
	for _, a := range need {
		if a.Verb == verbDelete {
			deleted[identity(a)] = true
		}
	}
	seen := make(map[changeset.Access]bool, len(need))
	var asks []changeset.Access
	for _, a := range need {
		if a.Verb == verbCreate {
			held, absent := s.snapshot(a)
			switch {
			case deleted[identity(a)] || absent || binding(a):
			case held:
				a.Verb = verbUpdate
			default:
				a.Verb = verbDryRun
			}
		}
		if a.Verb != verbDryRun && !binding(a) {
			a.Object = ""
		}
		if !seen[a] {
			seen[a] = true
			asks = append(asks, a)
		}
	}
	oks, errs := make([]bool, len(asks)), make([]error, len(asks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, authorConcurrency)
	for i, a := range asks {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			oks[i], errs[i] = s.allows(ctx, id, c, a)
		})
	}
	wg.Wait()
	var denied []string
	for i, a := range asks {
		switch {
		case errors.Is(errs[i], model.ErrInvalid), errors.Is(errs[i], model.ErrForbidden):
			return errs[i]
		case errs[i] != nil:
			return s.reviewFailed(ctx, id, errs[i])
		case !oks[i]:
			if a.Verb == verbDryRun {
				a.Verb = "create or update"
			}
			denied = append(denied, a.String())
		}
	}
	if len(denied) > 0 {
		return fmt.Errorf("%w: you may not %s, which this change makes the cluster do once merged",
			model.ErrForbidden, strings.Join(denied, ", or "))
	}
	return nil
}

// verbDryRun marks a create the snapshot cannot place: asked by a dry run.
const verbDryRun = "dry-run create"

// binding reports whether a is a RoleBinding carrying itself: weighed by the
// apiserver whatever its verb, as only it applies the escalation rule.
func binding(a changeset.Access) bool {
	return a.Object != "" && a.Group == model.KindRB.Group && a.Resource == model.KindRB.Plural
}

// snapshot is what the SA snapshots say of the object a names: held, or
// known absent. A project namespace or networking object can only be known
// held: the snapshot watches labelled namespaces alone, and a networking
// store may not have synced.
func (s *Server) snapshot(a changeset.Access) (held, absent bool) {
	if s.state != nil && s.state.Holds(a.Group, a.Resource, a.Namespace, a.Name) {
		return true, false
	}
	if s.netstate != nil && s.netstate.Holds(a.Group, a.Resource, a.Namespace, a.Name) {
		return true, false
	}
	vm := a.Group == model.KindVM.Group && a.Resource == model.KindVM.Plural
	return false, vm && s.state != nil && s.state.VMSnapshotReady()
}

// allows answers one access. A RoleBinding, and a create the snapshot cannot
// place, are weighed by the apiserver (dry runs); any other access asks for
// the whole resource first, the question the route gates cached, and for the
// one object only when that is denied, as RBAC may grant it by name. A create
// never names its object.
func (s *Server) allows(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	switch {
	case binding(a):
		return s.dryRun(ctx, id, c, a, s.weighBinding)
	case a.Verb == verbDryRun:
		return s.dryRun(ctx, id, c, a, s.weighCreate)
	}
	return s.updates(ctx, id, c, a)
}

// updates asks a review, the whole resource before the named object.
func (s *Server) updates(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	ref := ssarRef{a.Group, a.Resource}
	ok, err := s.can(ctx, id, c, a.Verb, ref, a.Namespace)
	if ok || err != nil || a.Name == "" || a.Verb == verbCreate {
		return ok, err
	}
	return s.canNamed(ctx, id, c, a.Verb, ref, a.Namespace, a.Name)
}

// dryRun caches weigh's verdict on a like a review, keyed by the object's
// content, for the short unwatchedTTL alone: a dry run answers for the object
// as it stands, which a delete or a create can change with no watch to say
// so.
func (s *Server) dryRun(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access, weigh func(context.Context, auth.Identity, *cluster.Client, changeset.Access) (bool, error)) (bool, error) {
	sum := sha256.Sum256([]byte(a.Object))
	ver := s.rbacVersion()
	k := strings.Join([]string{restfactory.TokenKey(id.Token), "dry-run", a.Verb, a.Group, a.Resource, a.Namespace, a.Name, hex.EncodeToString(sum[:])}, "\x00")
	if e, age, ok := s.ssar.GetAge(k); ok && e.ver == ver && age < s.maxAge(e.unsettled) {
		return e.ok, nil
	}
	e, err := review(ctx, s.ssar, k, ver, func(ctx context.Context) (ssarVerdict, error) {
		start := time.Now()
		ok, err := weigh(ctx, id, c, a)
		if err != nil {
			return ssarVerdict{}, err
		}
		e := ssarVerdict{ok: ok, ver: ver, unsettled: true}
		s.ssar.PutAt(k, e, start)
		return e, nil
	})
	return e.ok, err
}

// weighCreate dry-runs a create the snapshot cannot place, under the
// caller's token: the object found there is an update, asked as one. A
// create refused past authorization is refused here with the apiserver's
// reason: validation and admission answer before the apiserver looks for the
// object, and an admission plugin may weigh the caller too. Admission runs
// only webhooks declaring sideEffects None or NoneOnDryRun: a dry run reaches
// no other.
//
// The apiserver authorizes a create before it looks for the object, so a
// caller who may update an object but not create it is denied though Argo
// would patch it as them. A denied create is therefore asked as the update,
// and allowed when the object exists.
func (s *Server) weighCreate(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	version := apiVersionOf(a.Object)
	out, refused, err := c.DryRun(ctx, verbCreate, a.Group, version, a.Resource, a.Namespace, a.Name, []byte(a.Object))
	if err != nil {
		return false, err
	}
	if out == cluster.DryRunRefused {
		return false, fmt.Errorf("%w: %s %s/%s: %v", model.ErrInvalid, a.Resource, a.Namespace, a.Name, refused)
	}
	update := a
	update.Verb = verbUpdate
	switch out {
	case cluster.DryRunExists:
		return s.updates(ctx, id, c, update)
	case cluster.DryRunDenied:
		if ok, err := s.updates(ctx, id, c, update); !ok || err != nil {
			return false, err
		}
		return s.exists(ctx, a, version)
	}
	return true, nil
}

// exists asks dotvirt's SA whether the object a names exists: one GET of
// it, for a create the caller may not make but may make as an update. A
// kind the SA cannot read keeps the create's denial, saying why.
func (s *Server) exists(ctx context.Context, a changeset.Access, version string) (bool, error) {
	sa, err := s.clusterF.SA()
	if err != nil {
		return false, unavailable(err)
	}
	found, err := sa.Exists(ctx, a.Group, version, a.Resource, a.Namespace, a.Name)
	if apierrors.IsForbidden(err) {
		a.Verb = verbCreate
		return false, fmt.Errorf("%w: you may not %s, and dotvirt may not read whether it exists to weigh the change as its update",
			model.ErrForbidden, a)
	}
	return found, err
}

// weighBinding has the apiserver weigh a RoleBinding as Argo would apply it:
// the verb's RBAC and the escalation rule (bind on the role, or holding all
// it grants). A create that finds the binding there is asked again as the
// update it is. In a namespace the change creates no dry run reaches those
// checks, so their two halves are asked as reviews: the verb, and bind on
// the role by name. A binding refused past authorization is refused here:
// its escalation may not have been weighed.
func (s *Server) weighBinding(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	version := apiVersionOf(a.Object)
	out, refused, err := c.DryRun(ctx, a.Verb, a.Group, version, a.Resource, a.Namespace, a.Name, []byte(a.Object))
	if err == nil && out == cluster.DryRunExists {
		out, refused, err = c.DryRun(ctx, verbUpdate, a.Group, version, a.Resource, a.Namespace, a.Name, []byte(a.Object))
	}
	switch {
	case err != nil:
		return false, err
	case out == cluster.DryRunNoNamespace:
		return s.bindReviews(ctx, id, c, a)
	case out == cluster.DryRunRefused:
		return false, fmt.Errorf("%w: RoleBinding %s/%s: %v", model.ErrInvalid, a.Namespace, a.Name, refused)
	}
	return out == cluster.DryRunAllowed, nil
}

// bindReviews asks a RoleBinding's verb and bind on the role it references,
// read case-sensitively, as the apiserver reads it.
func (s *Server) bindReviews(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	kind, name := roleRefOf(a.Object)
	role := map[string]string{"ClusterRole": "clusterroles", "Role": "roles"}[kind]
	if role == "" || name == "" {
		return false, invalid(fmt.Errorf("RoleBinding %s references no Role or ClusterRole", a.Name))
	}
	if ok, err := s.can(ctx, id, c, a.Verb, ssarRef{a.Group, a.Resource}, a.Namespace); !ok || err != nil {
		return false, err
	}
	return s.canNamed(ctx, id, c, "bind", ssarRef{a.Group, role}, a.Namespace, name)
}

// roleRefOf is the role a binding references. Map keys match exactly, where
// decoding into a struct would match any case.
func roleRefOf(raw string) (kind, name string) {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return "", ""
	}
	ref, _ := obj["roleRef"].(map[string]any)
	kind, _ = ref["kind"].(string)
	name, _ = ref["name"].(string)
	return kind, name
}

// apiVersionOf is the version of the object raw holds, as it spells it.
func apiVersionOf(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return ""
	}
	v, _ := obj["apiVersion"].(string)
	if _, version, ok := strings.Cut(v, "/"); ok {
		return version
	}
	return v
}
