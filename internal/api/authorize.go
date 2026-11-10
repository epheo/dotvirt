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
	"sigs.k8s.io/yaml"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/drs"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// The access family. Every route gate, every capability field and the
// coordinator's gate (Authorize) weigh questions of one type, the
// coordinator's Access, on one path (allows): a create the SA snapshots can
// place is its update or its create, any other create of a known object is
// dry-run, a binding is weighed by the apiserver, and anything else is a
// cached review, the whole resource before the named object. A route gates
// what it alone decides (its tier, its verb, a namespace it claims), the
// coordinator each object a staged change writes, and the apiserver an
// imperative call; a capability field asks the union, so the UI offers an
// action only when every check standing in its way passes.

const (
	verbGet    = "get"
	verbList   = "list"
	verbCreate = "create"
	verbUpdate = "update"
	verbPatch  = "patch"
	verbDelete = "delete"
	verbBind   = "bind"
)

// question is one access as the coordinator's gate states it.
type question = changeset.Access

// action is the access one effect needs: every question allowed.
type action []question

// refQ is verb on ref in ns ("" for cluster scope).
func refQ(verb string, ref ssarRef, ns string) question {
	return question{Verb: verb, Group: ref.group, Resource: ref.resource, Namespace: ns}
}

func ask(verb string, ref ssarRef, ns string) action { return action{refQ(verb, ref, ns)} }

// ask answers one review: the whole resource first, the verdict most routes
// and signals share in the cache, and the named object only when that is
// denied, as RBAC may grant it by name. A create never names its object: the
// apiserver's own check of one does not. A bind is asked by its role's name
// alone: no route asks it of the whole resource, and RBAC grants it by name.
func (s *Server) ask(ctx context.Context, id auth.Identity, c *cluster.Client, q question) (bool, error) {
	q.Object = ""
	if q.Verb == verbCreate {
		q.Name = ""
	}
	// The apiserver authorizes a request on one Namespace object within that
	// namespace, so a RoleBinding there may grant it.
	if q.Group == ssarNamespace.group && q.Resource == ssarNamespace.resource && q.Name != "" {
		q.Namespace = q.Name
	}
	if q.Name == "" || q.Verb == verbBind {
		return s.can(ctx, id, c, q)
	}
	whole := q
	whole.Name = ""
	if ok, err := s.can(ctx, id, c, whole); ok || err != nil {
		return ok, err
	}
	return s.can(ctx, id, c, q)
}

// allowed answers act under id's token, each question placed and weighed as
// the coordinator's gate weighs it: ok, or the first question denied. An
// empty action is denied: every effect asks something.
func (s *Server) allowed(ctx context.Context, id auth.Identity, c *cluster.Client, act action) (question, bool, error) {
	if len(act) == 0 {
		return question{}, false, nil
	}
	for _, q := range act {
		ok, err := s.allows(ctx, id, c, s.placed(q, false))
		if err != nil || !ok {
			return q, false, err
		}
	}
	return question{}, true, nil
}

// needForChange and needForRead end a refusal: the first is what a change was
// missing, the second what a read was.
const (
	needForChange = "this change needs that right, read access is not enough"
	needForRead   = "reading this needs that right"
)

// require is allowed as a gate: nil, ErrForbidden naming the question denied,
// or the failed weighing.
func (s *Server) require(ctx context.Context, id auth.Identity, c *cluster.Client, act action) error {
	return s.requireFor(ctx, id, c, act, needForChange)
}

func (s *Server) requireFor(ctx context.Context, id auth.Identity, c *cluster.Client, act action, need string) error {
	q, ok, err := s.allowed(ctx, id, c, act)
	switch {
	case errors.Is(err, model.ErrInvalid), errors.Is(err, model.ErrForbidden):
		return err
	case err != nil:
		return s.reviewFailed(ctx, id, err)
	case !ok:
		return fmt.Errorf("%w: you may not %s; %s", model.ErrForbidden, q, need)
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
	return s.gateAnyFor(w, r, sc, acts, needForChange)
}

func (s *Server) gateAnyFor(w http.ResponseWriter, r *http.Request, sc scope, acts []action, need string) bool {
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
		fail(w, fmt.Errorf("%w: you may not %s; %s", model.ErrForbidden, strings.Join(asked, " or "), need))
		return false
	}
	return true
}

// capable is act as a capability field. A failed weighing reads as no: an
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

// authConcurrency bounds the reviews one request, frame or gated change fans
// out.
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
	ssarDataVolumes  = ssarRef{"cdi.kubevirt.io", "datavolumes"}
	ssarSnapshots    = ssarRef{"snapshot.kubevirt.io", "virtualmachinesnapshots"}
	ssarRestores     = ssarRef{"snapshot.kubevirt.io", "virtualmachinerestores"}
	ssarClones       = ssarRef{"clone.kubevirt.io", "virtualmachineclones"}
	ssarOAuthClients = ssarRef{"oauth.openshift.io", "oauthclients"}
	ssarUploadTokens = ssarRef{"upload.cdi.kubevirt.io", "uploadtokenrequests"}
)

// kubevirtSub is verb on a KubeVirt subresource in ns: virt-api authorizes
// each call as verb on resource/sub in the subresources group.
func kubevirtSub(verb, resource, sub, ns string) action {
	return action{{Verb: verb, Group: "subresources.kubevirt.io", Resource: resource, Subresource: sub, Namespace: ns}}
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
	restartAction         = func(ns string) action { return kubevirtSub(verbUpdate, "virtualmachines", "restart", ns) }
	migrateAction         = func(ns string) action { return kubevirtSub(verbUpdate, "virtualmachines", "migrate", ns) }
	pauseAction           = func(ns string) action { return kubevirtSub(verbUpdate, "virtualmachineinstances", "pause", ns) }
	unpauseAction         = func(ns string) action { return kubevirtSub(verbUpdate, "virtualmachineinstances", "unpause", ns) }
	consoleAction         = func(ns string) action { return kubevirtSub(verbGet, "virtualmachineinstances", "vnc", ns) }
	snapshotTakeAction    = func(ns string) action { return ask(verbCreate, ssarSnapshots, ns) }
	snapshotRestoreAction = func(ns string) action { return ask(verbCreate, ssarRestores, ns) }
	snapshotDeleteAction  = func(ns string) action { return ask(verbDelete, ssarSnapshots, ns) }
	cloneAction           = func(ns string) action { return ask(verbCreate, ssarClones, ns) }
	dataVolumeAction      = func(ns string) action {
		return action{refQ(verbCreate, ssarDataVolumes, ns), refQ(verbCreate, ssarUploadTokens, ns)}
	}
	// bootImageAction is an upload registered as a boot image: the upload, then
	// a DataSource over its claim.
	bootImageAction = func(ns string) action {
		return append(dataVolumeAction(ns), refQ(verbCreate, ssarDataSources, ns))
	}
	// bootImageImportAction is a boot image the cluster downloads itself: the
	// import DataVolume and its DataSource, no upload token.
	bootImageImportAction = func(ns string) action {
		return action{refQ(verbCreate, ssarDataVolumes, ns), refQ(verbCreate, ssarDataSources, ns)}
	}
	// bootImageDeleteAction removes an unfinished boot image: its DataSource
	// and its DataVolume (the claim goes with it), asked whole before the
	// first delete.
	bootImageDeleteAction = func(ns string) action {
		return action{refQ(verbDelete, ssarDataSources, ns), refQ(verbDelete, ssarDataVolumes, ns)}
	}
)

// claimAction is relabelling the existing namespace ns: what joining it to a
// project asks of the route. ns "" asks it of every namespace.
func claimAction(ns string) action {
	return action{{Verb: verbUpdate, Group: ssarNamespace.group, Resource: ssarNamespace.resource, Name: ns}}
}

// readNamespace is reading namespace ns under the caller's token, which every
// tenancy route does first to learn what it already carries: get, asked of
// the one name (ns "" asks it of every namespace, as a name no namespace
// holds yet).
func readNamespace(ns string) action {
	return action{{Verb: verbGet, Group: ssarNamespace.group, Resource: ssarNamespace.resource, Name: ns}}
}

// drsSchedulerActions open the enable route: creating the KubeDescheduler, or
// updating it when DRS is reconfigured.
func drsSchedulerActions() []action {
	q := refQ(verbCreate, ssarDescheduler, drs.Namespace)
	u := q
	u.Verb, u.Name = verbUpdate, drs.SchedulerName
	return []action{{q}, {u}}
}

// drsDisableAction removes the KubeDescheduler: the disable route's gate.
func drsDisableAction() action {
	q := refQ(verbDelete, ssarDescheduler, drs.Namespace)
	q.Name = drs.SchedulerName
	return action{q}
}

// oauthClientCap is the server-side apply of OAuthClient name as the
// apiserver weighs it: its patch, and its create unless it exists, the apply
// then only patching - asked as the gate asks a create the caller may not
// make (weighCreate).
func (s *Server) oauthClientCap(ctx context.Context, id auth.Identity, c *cluster.Client, name string) bool {
	patch := refQ(verbPatch, ssarOAuthClients, "")
	patch.Name = name
	if !s.capable(ctx, id, c, action{patch}) {
		return false
	}
	if s.capable(ctx, id, c, ask(verbCreate, ssarOAuthClients, "")) {
		return true
	}
	found, err := s.exists(ctx, patch, "v1")
	return err == nil && found
}

// nodeAction is a patch of node: cordon, maintenance, and the evacuation the
// maintenance panel drives.
func nodeAction(node string) action {
	q := refQ(verbPatch, ssarNodes, "")
	q.Name = node
	return action{q}
}

// formActions are the rights that open a create form on kind k in ns: the
// form creates, or edits the object git already declares, and the
// coordinator weighs which.
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
// the coordinator weighs the one the change needs, on the kind git declares.
func objectActions(verbs []string, resource, ns string) []action {
	res, _ := model.LookupResource(resource)
	clusterScoped := ns == model.ClusterScopeNS
	var acts []action
	for _, verb := range verbs {
		for _, k := range res.Kinds {
			if k.ClusterScoped == clusterScoped {
				acts = append(acts, kindAction(verb, k, ns))
			}
		}
	}
	return acts
}

// objectQuestion is creating the one object raw (YAML or JSON) declares, as
// the coordinator states a create: named, carrying the object for the dry
// run.
func objectQuestion(raw []byte) (question, error) {
	js, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return question{}, err
	}
	var body struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(js, &body); err != nil {
		return question{}, err
	}
	k, ok := managedKind(body.Kind)
	if !ok {
		return question{}, fmt.Errorf("%w: %s is not a kind dotvirt manages", model.ErrInvalid, body.Kind)
	}
	ns := body.Metadata.Namespace
	if k.ClusterScoped {
		ns = ""
	}
	return question{Verb: verbCreate, Group: k.Group, Resource: k.Plural, Namespace: ns, Name: body.Metadata.Name, Object: string(js)}, nil
}

// managedKind is the managed kind named kind.
func managedKind(kind string) (model.Kind, bool) {
	if kind == model.KindDV.Kind {
		return model.KindDV, true
	}
	_, k, ok := model.LookupKind(kind)
	return k, ok
}

// adoptableByRight keeps the captured objects the caller may bring under git,
// each weighed as the coordinator weighs adopting it - a create of an object
// that runs, so the update Argo makes of it - and names the kinds refused,
// left out of the adoption like the unreadable ones, so the rest of a
// namespace still comes under git. ok=false means the response is written: a
// failed weighing, or nothing left the caller may adopt.
func (s *Server) adoptableByRight(w http.ResponseWriter, r *http.Request, sc scope, where string, objs []model.Adoptable) ([]model.Adoptable, []string, bool) {
	if where == "" {
		where = "the cluster scope"
	}
	named := map[string]bool{}
	var kept []model.Adoptable
	var denied []string
	for _, o := range objs {
		q, err := objectQuestion(o.Manifest)
		label := q.Resource
		if err != nil {
			label = o.Kind
		} else if _, ok, aerr := s.allowed(r.Context(), sc.id, sc.cluster, action{q}); aerr != nil {
			fail(w, s.adoptFailed(r.Context(), sc.id, aerr))
			return nil, nil, false
		} else if ok {
			kept = append(kept, o)
			continue
		}
		if !named[label] {
			named[label] = true
			denied = append(denied, label)
		}
	}
	if len(kept) == 0 {
		fail(w, fmt.Errorf("%w: you may not bring %s in %s under git, so there is nothing adoptable you may adopt",
			model.ErrForbidden, strings.Join(denied, ", "), where))
		return nil, nil, false
	}
	return kept, denied, true
}

// adoptFailed is a weighing that failed: its own refusal (an object the
// apiserver refuses past authorization), or the review's outage.
func (s *Server) adoptFailed(ctx context.Context, id auth.Identity, err error) error {
	if errors.Is(err, model.ErrInvalid) || errors.Is(err, model.ErrForbidden) {
		return err
	}
	return s.reviewFailed(ctx, id, err)
}

// authorizeTimeout bounds the reviews one gated change asks for: the
// coordinator calls without the request's context.
const authorizeTimeout = 15 * time.Second

// Authorize implements changeset.Authorizer: every access a staged, proposed
// or reverted change makes Argo perform, placed and weighed as every route
// gate and capability field weighs its own (placed, allows), under id's own
// token, at most authConcurrency at once.
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
	denied, err := s.weighAll(ctx, id, c, need)
	switch {
	case errors.Is(err, model.ErrInvalid), errors.Is(err, model.ErrForbidden):
		return err
	case err != nil:
		return s.reviewFailed(ctx, id, err)
	case len(denied) > 0:
		return fmt.Errorf("%w: you may not %s, which this change makes the cluster do once merged",
			model.ErrForbidden, strings.Join(denied, ", or "))
	}
	return nil
}

// weighAll weighs every access of need, each distinct one once, at most
// authConcurrency at once: the ones denied, or the first weighing that
// failed. A create the change pairs with a delete is a replace and stays a
// create.
func (s *Server) weighAll(ctx context.Context, id auth.Identity, c *cluster.Client, need []changeset.Access) ([]string, error) {
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
		a = s.placed(a, deleted[identity(a)])
		if !seen[a] {
			seen[a] = true
			asks = append(asks, a)
		}
	}
	oks, errs := make([]bool, len(asks)), make([]error, len(asks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, authConcurrency)
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
		case errs[i] != nil:
			return nil, errs[i]
		case !oks[i]:
			if a.Verb == verbDryRun {
				a.Verb = "create or update"
			}
			denied = append(denied, a.String())
		}
	}
	return denied, nil
}

// needsAllowed is a change's accesses as a capability: every one allowed,
// as the coordinator's gate weighs them. A failure reads as no.
func (s *Server) needsAllowed(ctx context.Context, id auth.Identity, c *cluster.Client, need []changeset.Access, err error) bool {
	if err != nil {
		return false
	}
	denied, err := s.weighAll(ctx, id, c, need)
	return err == nil && len(denied) == 0
}

// verbDryRun marks a create the snapshot cannot place: asked by a dry run.
const verbDryRun = "dry-run create"

// placed is how a is weighed. A create may land on an object that already
// runs, which Argo then patches: one the SA snapshots hold is asked as that
// update; one they know absent (a VM, once its snapshot synced) or replaced
// in the same change is asked as the create; any other carrying its object is
// dry-run (weighCreate). A create of no object in particular (a capability's
// new namespace) is its review: a dry run of one that does not exist answers
// the same. A RoleBinding carrying itself is weighed by the apiserver
// whatever its verb.
func (s *Server) placed(a changeset.Access, replaced bool) changeset.Access {
	if a.Verb == verbCreate && a.Name != "" {
		held, absent := s.snapshot(a)
		switch {
		case replaced || absent || binding(a):
		case held:
			a.Verb = verbUpdate
		case a.Object != "":
			a.Verb = verbDryRun
		}
	}
	if a.Verb != verbDryRun && !binding(a) {
		a.Object = ""
	}
	return a
}

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

// allows weighs one placed access. A RoleBinding, and a create the snapshot
// cannot place, are weighed by the apiserver (dry runs); any other access is
// a review (ask).
func (s *Server) allows(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access) (bool, error) {
	switch {
	case binding(a):
		return s.dryRun(ctx, id, c, a, s.weighBinding)
	case a.Verb == verbDryRun:
		return s.dryRun(ctx, id, c, a, s.weighCreate)
	}
	return s.ask(ctx, id, c, a)
}

// dryRun caches weigh's verdict on a like a review, keyed by the object's
// content, for the short unwatchedTTL alone: a dry run answers for the object
// as it stands, which a delete or a create can change with no watch to say
// so. Its stamp is the grant version of every namespace weigh asks in: a's,
// and for a Namespace object the namespace itself, where the update it may
// be weighed as is asked (ask).
func (s *Server) dryRun(ctx context.Context, id auth.Identity, c *cluster.Client, a changeset.Access, weigh func(context.Context, auth.Identity, *cluster.Client, changeset.Access) (bool, error)) (bool, error) {
	sum := sha256.Sum256([]byte(a.Object))
	ver := grantStamps{ns: s.grantVersion(a.Namespace)}
	if a.Group == ssarNamespace.group && a.Resource == ssarNamespace.resource && a.Name != "" {
		ver.obj = s.grantVersion(a.Name)
	}
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
		return s.ask(ctx, id, c, update)
	case cluster.DryRunDenied:
		if ok, err := s.ask(ctx, id, c, update); !ok || err != nil {
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
	_, ok, err := s.allowed(ctx, id, c, action{
		{Verb: a.Verb, Group: a.Group, Resource: a.Resource, Namespace: a.Namespace},
		{Verb: verbBind, Group: a.Group, Resource: role, Namespace: a.Namespace, Name: name},
	})
	return ok, err
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

// authorizeLibrary gates a write to a project's template library, which no
// gate weighs: blueprints Argo never applies, so the project's authoring
// right (projectAuthor) stands for it.
func (s *Server) authorizeLibrary(w http.ResponseWriter, r *http.Request, sc scope) bool {
	ok, err := s.projectAuthor(r.Context(), sc.id, sc.cluster, sc.proj.Namespaces)
	if err != nil {
		fail(w, s.reviewFailed(r.Context(), sc.id, err))
		return false
	}
	if !ok {
		fail(w, fmt.Errorf("%w: you may not update virtualmachines in any namespace of %s; writing its library needs edit rights",
			model.ErrForbidden, sc.proj.Name))
		return false
	}
	return true
}

// projectAuthor is authoring rights in any of nss: the gate on a project's
// own template library.
func (s *Server) projectAuthor(ctx context.Context, id auth.Identity, c *cluster.Client, nss []string) (bool, error) {
	acts := make([]action, 0, len(nss))
	for _, ns := range nss {
		acts = append(acts, vmAction(verbUpdate, ns))
	}
	return s.anyAllowed(ctx, id, c, acts)
}

// gateRepo refuses a caller who may not read what a tenant project's repo holds
// in namespace, or in every namespace of the project when it is "": the repo
// holds those objects, so the apiserver's answer on each declared kind is the
// answer on the repo. A namespace they cannot read is refused without being
// named. The platform tier answered this at its own scope (canReadPlatform).
func (s *Server) gateRepo(w http.ResponseWriter, r *http.Request, sc scope, namespace string) bool {
	if err := s.readsRepo(r.Context(), sc, namespace); err != nil {
		fail(w, err)
		return false
	}
	return true
}

// readsRepo is gateRepo's verdict: nil, ErrForbidden, or the failed weighing.
func (s *Server) readsRepo(ctx context.Context, sc scope, namespace string) error {
	if sc.proj.Name == platformProjectName {
		return nil
	}
	namespaces := []string{namespace}
	if namespace == "" {
		if seesPartOf(s.AllProjects(), sc.proj) {
			return fmt.Errorf("%w: this needs every namespace of %s in your view", model.ErrForbidden, sc.proj.Name)
		}
		namespaces = sc.proj.Namespaces
	}
	act, err := s.repoListing(ctx, sc.proj, namespaces)
	if err != nil || len(act) == 0 {
		return err
	}
	return s.requireFor(ctx, sc.id, sc.cluster, act, needForRead)
}

// authorNamespaces is the UI's authoring signal for nss. One cluster-wide
// review answers for every namespace when it allows; otherwise each namespace
// is reviewed, at most authConcurrency at once. A namespace whose review
// fails is left out of the map, so a timeout reads as unknown, never as no.
func (s *Server) authorNamespaces(ctx context.Context, id auth.Identity, c *cluster.Client, nss []string) map[string]bool {
	out := make(map[string]bool, len(nss))
	authorIn := vmAction
	if s.noVMs() {
		// admin and edit grant this on any Kubernetes; see visibilityRef.
		authorIn = func(verb, ns string) action { return ask(verb, ssarNetpol, ns) }
	}
	if _, all, err := s.allowed(ctx, id, c, authorIn(verbUpdate, "")); err == nil && all {
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
			_, ok, err := s.allowed(ctx, id, c, authorIn(verbUpdate, ns))
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
