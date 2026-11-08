package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/drs"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netgen"
)

// The access-review family. Every route gate and every capability field the
// API exposes read one verdict, allowed, over the cached reviews (can), asked
// as the coordinator's gate asks its own (ask): the UI offers an action only
// when every check standing in its way passes. A route gates what it alone
// decides (its tier, its verb, a namespace it claims); the coordinator's gate
// then checks each object a staged change writes, and an imperative act is
// checked by the apiserver on the call itself. A capability field asks the
// union: the route's questions and the ones the change makes the coordinator
// or the apiserver ask.

const (
	verbGet    = "get"
	verbList   = "list"
	verbCreate = "create"
	verbUpdate = "update"
	verbPatch  = "patch"
	verbDelete = "delete"
	verbBind   = "bind"
)

// question is one access review: verb on ref in ns ("" for cluster scope),
// narrowed to one object when name is set.
type question struct {
	verb string
	ref  ssarRef
	ns   string
	name string
}

func (q question) String() string {
	if q.verb == "" {
		return "do this"
	}
	what := q.ref.resource
	if q.ref.sub != "" {
		what += "/" + q.ref.sub
	}
	if q.name != "" {
		what += " " + q.name
	}
	if q.ns != "" {
		return q.verb + " " + what + " in " + q.ns
	}
	return q.verb + " " + what
}

// action is the access one effect needs: every question allowed.
type action []question

func ask(verb string, ref ssarRef, ns string) action {
	return action{{verb: verb, ref: ref, ns: ns}}
}

// ask answers one question: the whole resource first, the verdict most
// routes and signals share in the cache, and the named object only when that
// is denied, as RBAC may grant it by name.
func (s *Server) ask(ctx context.Context, id auth.Identity, c *cluster.Client, q question) (bool, error) {
	if q.name == "" {
		return s.can(ctx, id, c, q)
	}
	whole := q
	whole.name = ""
	if ok, err := s.can(ctx, id, c, whole); ok || err != nil {
		return ok, err
	}
	return s.can(ctx, id, c, q)
}

// allowed answers act under id's token: ok, or the first question denied. An
// empty action is denied: every effect asks something.
func (s *Server) allowed(ctx context.Context, id auth.Identity, c *cluster.Client, act action) (question, bool, error) {
	if len(act) == 0 {
		return question{}, false, nil
	}
	for _, q := range act {
		ok, err := s.ask(ctx, id, c, q)
		if err != nil || !ok {
			return q, false, err
		}
	}
	return question{}, true, nil
}

// require is allowed as a gate: nil, ErrForbidden naming the question denied,
// or the failed review.
func (s *Server) require(ctx context.Context, id auth.Identity, c *cluster.Client, act action) error {
	q, ok, err := s.allowed(ctx, id, c, act)
	if err != nil {
		return s.reviewFailed(ctx, id, err)
	}
	if !ok {
		return fmt.Errorf("%w: you may not %s; this change needs that right, read access is not enough", model.ErrForbidden, q)
	}
	return nil
}

// gate is require answering the request: ok=false means the refusal is written.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, sc scope, act action) bool {
	if err := s.require(r.Context(), sc.id, sc.cluster, act); err != nil {
		fail(w, err)
		return false
	}
	return true
}

// gateAny gates a route on any of acts: the coordinator then checks the one
// the change turns out to need (create or update, the kind git declares).
func (s *Server) gateAny(w http.ResponseWriter, r *http.Request, sc scope, acts []action) bool {
	ok, err := s.anyAllowed(r.Context(), sc.id, sc.cluster, acts)
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return false
	}
	if !ok {
		var asked []string
		for _, act := range acts {
			if len(act) > 0 {
				asked = append(asked, act[0].String())
			}
		}
		fail(w, fmt.Errorf("%w: you may not %s; this change needs that right, read access is not enough",
			model.ErrForbidden, strings.Join(asked, " or ")))
		return false
	}
	return true
}

// capable is act as a capability field. A failed review reads as no: an
// unknown never offers an action.
func (s *Server) capable(ctx context.Context, id auth.Identity, c *cluster.Client, act action) bool {
	_, ok, _ := s.allowed(ctx, id, c, act)
	return ok
}

// anyAllowed reports whether any of acts is allowed. A failed review is
// returned only when none is.
func (s *Server) anyAllowed(ctx context.Context, id auth.Identity, c *cluster.Client, acts []action) (bool, error) {
	var failed error
	for _, act := range acts {
		_, ok, err := s.allowed(ctx, id, c, act)
		if ok {
			return true, nil
		}
		if err != nil {
			failed = err
		}
	}
	return false, failed
}

// authConcurrency bounds the reviews one request or frame fans out.
const authConcurrency = 8

// capableAll answers each of acts as a capability, at most authConcurrency
// at once.
func (s *Server) capableAll(ctx context.Context, id auth.Identity, c *cluster.Client, acts []action) []bool {
	out := make([]bool, len(acts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, authConcurrency)
	for i, act := range acts {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.capable(ctx, id, c, act)
		})
	}
	wg.Wait()
	return out
}

// The effects, each spelled once and read by both the route that performs
// one and the capability field that offers it.

var (
	ssarVM           = ssarFor(model.KindVM)
	ssarRoleBindings = ssarFor(model.KindRB)
	ssarClusterRoles = ssarRef{group: "rbac.authorization.k8s.io", resource: "clusterroles"}
	ssarDataVolumes  = ssarRef{group: "cdi.kubevirt.io", resource: "datavolumes"}
	ssarSnapshots    = ssarRef{group: "snapshot.kubevirt.io", resource: "virtualmachinesnapshots"}
	ssarRestores     = ssarRef{group: "snapshot.kubevirt.io", resource: "virtualmachinerestores"}
	ssarClones       = ssarRef{group: "clone.kubevirt.io", resource: "virtualmachineclones"}
	ssarOAuthClients = ssarRef{group: "oauth.openshift.io", resource: "oauthclients"}
)

// kubevirtSub is a KubeVirt subresource: virt-api authorizes each call as
// verb on resource/sub in the subresources group.
func kubevirtSub(resource, sub string) ssarRef {
	return ssarRef{group: "subresources.kubevirt.io", resource: resource, sub: sub}
}

// vmAction is verb on the VirtualMachines of ns: create (a new VM, a template
// deploy), update (edit, adopt, restore, resync), delete.
func vmAction(verb, ns string) action { return ask(verb, ssarVM, ns) }

// kindAction is verb on kind k in ns; a cluster-scoped kind ignores ns.
func kindAction(verb string, k model.Kind, ns string) action {
	if k.ClusterScoped {
		ns = ""
	}
	return ask(verb, ssarFor(k), ns)
}

// The imperative VM operations run under the caller's token: each is the
// question the apiserver asks of the call itself.
var (
	restartAction         = func(ns string) action { return ask(verbUpdate, kubevirtSub("virtualmachines", "restart"), ns) }
	migrateAction         = func(ns string) action { return ask(verbUpdate, kubevirtSub("virtualmachines", "migrate"), ns) }
	pauseAction           = func(ns string) action { return ask(verbUpdate, kubevirtSub("virtualmachineinstances", "pause"), ns) }
	unpauseAction         = func(ns string) action { return ask(verbUpdate, kubevirtSub("virtualmachineinstances", "unpause"), ns) }
	consoleAction         = func(ns string) action { return ask(verbGet, kubevirtSub("virtualmachineinstances", "vnc"), ns) }
	snapshotTakeAction    = func(ns string) action { return ask(verbCreate, ssarSnapshots, ns) }
	snapshotRestoreAction = func(ns string) action { return ask(verbCreate, ssarRestores, ns) }
	snapshotDeleteAction  = func(ns string) action { return ask(verbDelete, ssarSnapshots, ns) }
	cloneAction           = func(ns string) action { return ask(verbCreate, ssarClones, ns) }
	dataVolumeAction      = func(ns string) action { return ask(verbCreate, ssarDataVolumes, ns) }
)

// claimAction is what joining an existing namespace to a project asks of the
// route: rewriting its labels. A new one asks nothing here: the coordinator
// checks creating it, with whatever else its file declares.
func claimAction(ns string) action {
	return action{{verb: verbUpdate, ref: ssarNamespace, name: ns}}
}

// grantAdminAction is the owners' RoleBinding in ns to the admin ClusterRole:
// the binding, and bind on that role, the escalation check the apiserver runs
// on it.
func grantAdminAction(ns string) action {
	return action{
		{verb: verbCreate, ref: ssarRoleBindings, ns: ns},
		{verb: verbBind, ref: ssarClusterRoles, ns: ns, name: netgen.OwnerRole},
	}
}

// newNamespaceAction is staging a namespace that does not exist yet, as the
// coordinator checks it: creating it, its owners' RoleBinding, and its
// primary VM Network. It asks cluster-wide: a namespace that does not exist
// holds no RoleBinding, so that verdict is each new one's.
func newNamespaceAction(owners, vmNetwork bool) action {
	act := action{{verb: verbCreate, ref: ssarNamespace}}
	if owners {
		act = append(act, grantAdminAction("")...)
	}
	if vmNetwork {
		act = append(act, kindAction(verbCreate, model.KindUDN, "")...)
	}
	return act
}

// relabelAction is rewriting the tenancy of every namespace in nss, as
// releasing a project does.
func relabelAction(nss []string) action {
	act := make(action, 0, len(nss))
	for _, ns := range nss {
		act = append(act, claimAction(ns)...)
	}
	return act
}

// drsCreateAction is the enable route's gate: creating the KubeDescheduler.
func drsCreateAction() action {
	return action{{verb: verbCreate, ref: ssarDescheduler, ns: drs.Namespace}}
}

// drsEnableAction is enabling DRS: the route's gate and the file set the
// coordinator checks - the operator's namespace, OperatorGroup and
// Subscription, the KubeDescheduler, and with psi the worker MachineConfig
// that reboots the pool.
func drsEnableAction(psi bool) action {
	act := append(drsCreateAction(),
		question{verb: verbCreate, ref: ssarNamespace},
		question{verb: verbCreate, ref: ssarFor(model.KindOpGroup), ns: drs.Namespace},
		question{verb: verbCreate, ref: ssarFor(model.KindSub), ns: drs.Namespace},
	)
	if psi {
		act = append(act, question{verb: verbCreate, ref: ssarMachineCfg})
	}
	return act
}

// drsDisableAction removes the KubeDescheduler, the disable route's gate and
// the one object the coordinator then checks; the operator install stays.
func drsDisableAction() action {
	return action{{verb: verbDelete, ref: ssarDescheduler, ns: drs.Namespace, name: drs.SchedulerName}}
}

// oauthClientAction is the server-side apply of OAuthClient name: a patch,
// which creates it when absent.
func oauthClientAction(name string) action {
	return action{
		{verb: verbCreate, ref: ssarOAuthClients},
		{verb: verbPatch, ref: ssarOAuthClients, name: name},
	}
}

// nodeAction is a patch of node: cordon, maintenance, and the evacuation the
// maintenance panel drives.
func nodeAction(node string) action {
	return action{{verb: verbPatch, ref: ssarNodes, name: node}}
}

// formActions are the rights that open a create form on kind k in ns: the
// form creates, or edits the object git already declares, and the
// coordinator checks which.
func formActions(k model.Kind, ns string) []action {
	return []action{kindAction(verbCreate, k, ns), kindAction(verbUpdate, k, ns)}
}

// gateForm gates a namespaced create form on formActions.
func (s *Server) gateForm(w http.ResponseWriter, r *http.Request, sc scope, k model.Kind, ns string) bool {
	return s.gateAny(w, r, sc, formActions(k, ns))
}

// platformForm resolves the platform tier for a cluster-scoped create form on
// k, opened by formActions.
func (s *Server) platformForm(w http.ResponseWriter, r *http.Request, k model.Kind) (scope, bool) {
	return s.platformScopeWith(w, r, func(ctx context.Context, id auth.Identity, c *cluster.Client) (bool, error) {
		return s.anyAllowed(ctx, id, c, formActions(k, ""))
	}, "not authorized to create or update "+k.Plural)
}

// objectActions are each of verbs on each backing resource may take in ns
// (model.ClusterScopeNS for the cluster scope): the route opens on any, and
// the coordinator checks the one the change needs, on the kind git declares.
func objectActions(verbs []string, resource, ns string) []action {
	res, _ := model.LookupResource(resource)
	cluster := ns == model.ClusterScopeNS
	var acts []action
	for _, verb := range verbs {
		for _, k := range res.Kinds {
			if k.ClusterScoped == cluster {
				acts = append(acts, kindAction(verb, k, ns))
			}
		}
	}
	return acts
}

// kindRef is the authority over a managed kind, ok=false for one dotvirt
// does not manage.
func kindRef(kind string) (ssarRef, bool) {
	if kind == "DataVolume" {
		return ssarDataVolumes, true
	}
	if _, k, ok := model.LookupKind(kind); ok {
		return ssarFor(k), true
	}
	return ssarRef{}, false
}

// adoptableByRight keeps the captured objects the caller may create - what
// the coordinator asks of a file git does not declare yet - and names the
// kinds refused, left out of the adoption like the unreadable ones, so the
// rest of a namespace still comes under git. ok=false means the response is
// written: a failed review, or nothing left the caller may adopt.
func (s *Server) adoptableByRight(w http.ResponseWriter, r *http.Request, sc scope, where string, objs []model.Adoptable) ([]model.Adoptable, []string, bool) {
	if where == "" {
		where = "the cluster scope"
	}
	verdict := map[question]bool{}
	named := map[string]bool{}
	var kept []model.Adoptable
	var denied []string
	for _, o := range objs {
		ref, known := kindRef(o.Kind)
		q := question{verb: verbCreate, ref: ref, ns: o.Namespace}
		ok, asked := verdict[q]
		if known && !asked {
			var err error
			if _, ok, err = s.allowed(r.Context(), sc.id, sc.cluster, action{q}); err != nil {
				fail(w, s.reviewFailed(r.Context(), sc.id, err))
				return nil, nil, false
			}
			verdict[q] = ok
		}
		if ok {
			kept = append(kept, o)
			continue
		}
		label := ref.resource
		if !known {
			label = o.Kind
		}
		if !named[label] {
			named[label] = true
			denied = append(denied, label)
		}
	}
	if len(kept) == 0 {
		fail(w, fmt.Errorf("%w: you may not create %s in %s, so there is nothing adoptable you may bring under git",
			model.ErrForbidden, strings.Join(denied, ", "), where))
		return nil, nil, false
	}
	return kept, denied, true
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
	ok, err := s.projectAuthor(r.Context(), sc.id, sc.cluster, sc.proj.Namespaces)
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: you may not update virtualmachines in any namespace of %s; proposing changes needs edit rights",
			model.ErrForbidden, sc.proj.Name))
		return false
	}
	return true
}

// projectAuthor is authoring rights in any of nss: the gate on a project's
// own template library and on its proposals.
func (s *Server) projectAuthor(ctx context.Context, id auth.Identity, c *cluster.Client, nss []string) (bool, error) {
	acts := make([]action, 0, len(nss))
	for _, ns := range nss {
		acts = append(acts, vmAction(verbUpdate, ns))
	}
	return s.anyAllowed(ctx, id, c, acts)
}

// authorizeRepo gates a write that rewrites repo content no per-change gate
// saw (revert): authoring rights in every namespace of the project, the ones
// hidden from the caller included, since a commit may touch any of them.
func (s *Server) authorizeRepo(w http.ResponseWriter, r *http.Request, sc scope) bool {
	if sc.platform {
		return true
	}
	// A namespace the caller cannot read is one they cannot author; it is
	// refused without being named.
	if full, ok := s.projectByName(sc.proj.Name); ok && len(full.Namespaces) > len(sc.proj.Namespaces) {
		fail(w, fmt.Errorf("%w: reverting needs edit rights in every namespace of %s", model.ErrForbidden, sc.proj.Name))
		return false
	}
	for _, ns := range sc.proj.Namespaces {
		if !s.gate(w, r, sc, vmAction(verbUpdate, ns)) {
			return false
		}
	}
	return true
}

// authorNamespaces is the UI's authoring signal for nss. One cluster-wide
// review answers for every namespace when it allows; otherwise each namespace
// is reviewed, at most authConcurrency at once. A namespace whose review
// fails is left out of the map, so a timeout reads as unknown, never as no.
func (s *Server) authorNamespaces(ctx context.Context, id auth.Identity, c *cluster.Client, nss []string) map[string]bool {
	out := make(map[string]bool, len(nss))
	if _, all, err := s.allowed(ctx, id, c, vmAction(verbUpdate, "")); err == nil && all {
		for _, ns := range nss {
			out[ns] = true
		}
		return out
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, authConcurrency)
	)
	for _, ns := range nss {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			_, ok, err := s.allowed(ctx, id, c, vmAction(verbUpdate, ns))
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
