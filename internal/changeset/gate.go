package changeset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/manifest"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// The gate every write to a repo passes, at stage and again at propose: what
// a file will declare is read the way Argo reads it, must be exactly what the
// change claims, and each object it creates, changes or removes must be one
// the caller may create, update or delete themselves. Argo applies a merged
// file with its own rights, so this is where a user's RBAC bounds a change.

// Access is one act a change makes the cluster perform once merged, as an
// access review asks it. Name is set for a create too: the authorizer reads a
// create of an object that already runs as the update Argo makes of it, and
// leaves the name out of the review, as the apiserver's own check of a create
// does. Object, set for a RoleBinding, is the binding as Argo applies it: the
// authorizer has the apiserver weigh it, escalation rule included.
// Subresource is set only for an act the API asks of the apiserver directly
// (a VM restart), never by a change.
type Access struct {
	Verb, Group, Resource, Subresource, Namespace, Name string
	Object                                              string
}

func (a Access) String() string {
	what := a.Resource
	if a.Subresource != "" {
		what += "/" + a.Subresource
	}
	if a.Name != "" {
		what += " " + a.Name
	}
	if a.Namespace == "" {
		return a.Verb + " " + what
	}
	return a.Verb + " " + what + " in " + a.Namespace
}

// Authorizer asks the cluster whether id may perform every access, under id's
// own token: nil when all are allowed, else model.ErrForbidden naming one that
// is not, or the review's own failure. The API implements it over its cached
// access reviews.
type Authorizer interface {
	Authorize(id auth.Identity, need []Access) error
}

// UseAuthorizer attaches the caller's RBAC to every stage, propose, restore
// and revert. Set after construction: the API server implementing it is built
// over the coordinator. Until it is set every write is refused.
func (c *Coordinator) UseAuthorizer(a Authorizer) { c.authz = a }

func (c *Coordinator) authorize(id auth.Identity, need []Access) error {
	if c.authz == nil {
		return fmt.Errorf("%w: no authorizer is configured, so no change can be made", model.ErrUnavailable)
	}
	if len(need) == 0 {
		return nil
	}
	return c.authz.Authorize(id, need)
}

// gate refuses entries whose files say more than the entries claim, or would
// make the cluster do what id may not; proj's base branch is what they land
// on. It pins each entry writing or removing a file to the version of that
// file it weighed (draft.Entry.BaseBlob) and answers *git.BaseMovedError for
// one pinned to another: its accesses would be read off the wrong version.
func (c *Coordinator) gate(id auth.Identity, proj project.ProjectInfo, entries []draft.Entry) error {
	need, err := c.needs(proj, entries)
	if err != nil {
		return err
	}
	return c.authorize(id, need)
}

// needs is every access committing entries to proj's base branch makes the
// cluster perform once merged.
func (c *Coordinator) needs(proj project.ProjectInfo, entries []draft.Entry) ([]Access, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	read, err := c.read(proj)
	if err != nil {
		return nil, err
	}
	idx, err := indexDeclared(read, c.baseBranch)
	if err != nil {
		return nil, err
	}
	var need []Access
	for i := range entries {
		a, err := c.entryAccess(read, idx, proj, &entries[i])
		if err != nil {
			return nil, err
		}
		need = append(need, a...)
	}
	return need, nil
}

// write is how an entry changes its file: the one reading the gate weighs
// and propose commits (toChangesetItems).
type write int

const (
	noWrite    write = iota
	wholeFile        // Manifest replaces the file
	fieldWrite       // Edit is applied to the VM in the file
	removal          // the file goes
)

func writeOf(e draft.Entry) write {
	switch {
	case e.Kind == draft.KindDelete:
		return removal
	case e.Manifest != "" && (e.Kind == draft.KindCreate || e.Kind == draft.KindEdit):
		return wholeFile
	case e.Kind == draft.KindEdit && e.Edit != nil:
		return fieldWrite
	}
	return noWrite
}

// entryAccess is what committing e does to the cluster.
func (c *Coordinator) entryAccess(read *git.Repo, idx declaredIndex, proj project.ProjectInfo, e *draft.Entry) ([]Access, error) {
	if e.Manifest != "" && e.Edit != nil {
		return nil, fmt.Errorf("%w: %s carries both a manifest and a settings edit; unstage it and make the change again", model.ErrInvalid, objectLabel(*e))
	}
	switch writeOf(*e) {
	case wholeFile:
		before, err := c.based(read, e)
		if err != nil {
			return nil, err
		}
		return fileAccess(proj, idx, fileChange{path: e.SourceFile, before: before, after: []byte(e.Manifest)}, expected(*e))
	case removal:
		before, err := c.based(read, e)
		if err != nil || before == nil {
			return nil, err
		}
		return fileAccess(proj, idx, fileChange{path: e.SourceFile, before: before, remove: true}, nil)
	case fieldWrite:
		ref, ok := declaredRef(idx, draft.ResourceVM, e.Namespace, e.Name)
		if !ok {
			return []Access{access(verbUpdate, model.KindVM, model.ObjectRef{Namespace: e.Namespace, Name: e.Name})}, nil
		}
		content, err := read.FileOnBranch(c.baseBranch, idx.Files[ref])
		if err != nil {
			return nil, err
		}
		return fieldEditAccess(idx.Files[ref], content, e.Namespace, e.Name)
	}
	return nil, nil
}

// based is what base holds at e's file (nil when absent), the version its
// accesses are read off: e is pinned to it when unpinned, and refused when
// pinned to another. A removal whose file is gone already is satisfied, as
// the commit's own pin check reads it.
func (c *Coordinator) based(read *git.Repo, e *draft.Entry) ([]byte, error) {
	content, ok, err := read.LookupOnBranch(c.baseBranch, e.SourceFile)
	if err != nil {
		return nil, err
	}
	pin := pinOf(content, ok)
	switch {
	case e.BaseBlob == "":
		e.BaseBlob = pin
	case e.BaseBlob != pin && (ok || e.Kind != draft.KindDelete):
		return nil, &movedPin{BaseMovedError: git.BaseMovedError{Path: e.SourceFile}, base: c.baseBranch}
	}
	if !ok {
		return nil, nil
	}
	return content, nil
}

// requirePinned refuses an entry writing or removing a file without a pin:
// one staged before entries were pinned, which the gate would weigh against
// one version and the commit apply to whatever base holds then.
func requirePinned(entries []draft.Entry) error {
	for _, e := range entries {
		if w := writeOf(e); (w == wholeFile || w == removal) && e.BaseBlob == "" {
			return fmt.Errorf("%w: %s was staged without the version it was made from; unstage it and stage it again", model.ErrConflict, objectLabel(e))
		}
	}
	return nil
}

// movedPin is an entry pinned to a version of its file base has moved past:
// a conflict to the stager, and to propose the *git.BaseMovedError the commit
// would meet.
type movedPin struct {
	git.BaseMovedError
	base string
}

func (m *movedPin) Error() string {
	return fmt.Sprintf("%v: %s changed on %s after this change was made from it; make the change again on the current version", model.ErrConflict, m.Path, m.base)
}

func (m *movedPin) Unwrap() []error { return []error{model.ErrConflict, &m.BaseMovedError} }

// fieldEditAccess is a settings edit's access, read off the VM as content
// declares it: its update, or its delete and create when Argo replaces it,
// disks from its templates included. What the edit writes is checked where it
// is applied: staging's dry run and the commit (keepsDeclared).
func fieldEditAccess(path string, content []byte, namespace, name string) ([]Access, error) {
	objs, err := argoObjects(path, content)
	if err != nil {
		return nil, fmt.Errorf("%w: %s as it stands %v; change it in git", model.ErrConflict, path, err)
	}
	for _, o := range objs {
		if o.kind == model.KindVM && o.ref.Namespace == namespace && o.ref.Name == name {
			return objectAccess(o, true)
		}
	}
	return nil, fmt.Errorf("%w: %s no longer declares %s/%s; try again", model.ErrConflict, path, namespace, name)
}

// keepsDeclared refuses a settings edit's result unless, read as Argo reads
// it, it declares what the file declared and passes the placement guard.
// manifest.ApplyEdit already holds the edit to the VM's own fields in its own
// decoding; this is Argo's, so a value breaking into another document adds no
// object no gate weighed.
func keepsDeclared(proj project.ProjectInfo, path string, content, out []byte) error {
	before, err := argoObjects(path, content)
	if err != nil {
		return fmt.Errorf("%w: %s as it stands %v; change it in git", model.ErrConflict, path, err)
	}
	after, err := argoObjects(path, out)
	if err == nil && len(after) == len(before) {
		for i, o := range after {
			if o.ref != before[i].ref {
				err = errors.New("declares other objects")
			}
		}
	}
	if err != nil || len(after) != len(before) {
		return fmt.Errorf("%w: the settings edit would change what %s declares", model.ErrInvalid, path)
	}
	return requirePlacement(proj, path, out)
}

const (
	verbCreate = "create"
	verbUpdate = "update"
	verbDelete = "delete"
)

// fileChange is one file write: what base holds at path (nil when absent) and
// what the write leaves there.
type fileChange struct {
	path          string
	before, after []byte
	remove        bool
}

// fileAccess validates what ch leaves at its path and derives the accesses it
// makes. An object the branch does not declare is created; one it declares,
// in this file or another, is updated when this file changes it; one only
// this file declared, and no longer does, is deleted. An object Argo replaces
// rather than patches (see replaced) is deleted and created. want, when set,
// is the identity the write claims; a revert claims none and may restore any
// managed object.
func fileAccess(proj project.ProjectInfo, idx declaredIndex, ch fileChange, want func(after []object) error) ([]Access, error) {
	var before, after []object
	if ch.before != nil {
		var err error
		if before, err = argoObjects(ch.path, ch.before); err != nil {
			return nil, fmt.Errorf("%w: %s as it stands %v; change it in git", model.ErrConflict, ch.path, err)
		}
	}
	if !ch.remove {
		var err error
		if after, err = argoObjects(ch.path, ch.after); err != nil {
			return nil, fmt.Errorf("%w: %s %v", model.ErrInvalid, ch.path, err)
		}
	}
	if git.InTemplatesDir(ch.path) {
		return libraryAccess(proj, ch, before, after, want)
	}
	if !ch.remove {
		if err := requirePlacement(proj, ch.path, ch.after); err != nil {
			return nil, err
		}
	}
	if want != nil {
		if err := want(after); err != nil {
			return nil, err
		}
	}
	was := make(map[model.ObjectRef]object, len(before))
	for _, o := range before {
		was[o.ref] = o
	}
	declared := make(map[model.ObjectRef]bool, len(after))
	var need []Access
	for _, o := range after {
		if declared[o.ref] {
			return nil, fmt.Errorf("%w: %s declares %s %s twice", model.ErrInvalid, ch.path, o.ref.Kind, o.ref.Name)
		}
		declared[o.ref] = true
		prev, inFile := was[o.ref]
		_, onBranch := idx.Files[o.ref]
		if inFile && reflect.DeepEqual(prev.body, o.body) {
			continue
		}
		a, err := objectAccess(o, inFile || onBranch)
		if err != nil {
			return nil, err
		}
		need = append(need, a...)
	}
	for _, o := range before {
		if declared[o.ref] {
			continue
		}
		if p, ok := idx.Files[o.ref]; ok && p != ch.path {
			continue
		}
		need = append(need, access(verbDelete, o.kind, o.ref))
	}
	return need, nil
}

// objectAccess is what Argo applying o does: a create, or an update when the
// object is declared already, or a delete and a create when Argo replaces it.
// A RoleBinding carries itself, for the apiserver to weigh the escalation
// rule bindings face.
func objectAccess(o object, declared bool) ([]Access, error) {
	verb := verbCreate
	if declared {
		verb = verbUpdate
	}
	var need []Access
	if replaced(o) {
		verb = verbCreate
		need = append(need, access(verbDelete, o.kind, o.ref))
	}
	a := access(verb, o.kind, o.ref)
	if verb == verbCreate || o.kind == model.KindRB {
		raw, err := json.Marshal(o.body)
		if err != nil {
			return nil, err
		}
		a.Object = string(raw)
	}
	return append(need, a), nil
}

// replaced reports whether Argo deletes and re-creates o instead of patching
// it: a sync option forcing a replace, or a hook, which a sync creates anew
// and deletes by its policy.
func replaced(o object) bool {
	meta, _ := o.body["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	for k, v := range annotations {
		value, _ := v.(string)
		switch {
		case k == "argocd.argoproj.io/sync-options" && (strings.Contains(value, "Replace=true") || strings.Contains(value, "Force=true")):
			return true
		case strings.HasPrefix(k, "argocd.argoproj.io/hook"), strings.HasPrefix(k, "helm.sh/hook"):
			return true
		}
	}
	return false
}

// libraryAccess gates a write under templates/: blueprints Argo never
// applies, so they carry no namespace and touch no object. The shared library
// is the platform repo's, written with the right to create templates
// cluster-wide, as its routes ask; a project's library is gated by the
// project's authoring right at its routes and at propose.
func libraryAccess(proj project.ProjectInfo, ch fileChange, before, after []object, want func(after []object) error) ([]Access, error) {
	for _, o := range slices.Concat(before, after) {
		if o.kind != model.KindTemplate {
			return nil, fmt.Errorf("%w: %s holds a %s; the template library holds only %s", model.ErrInvalid, ch.path, o.ref.Kind, model.KindTemplate.Kind)
		}
	}
	if want != nil {
		if err := want(after); err != nil {
			return nil, err
		}
	}
	if proj.Name != project.PlatformName {
		return nil, nil
	}
	return []Access{{Verb: verbCreate, Group: model.KindTemplate.Group, Resource: model.KindTemplate.Plural}}, nil
}

// access is verb on the object ref names.
func access(verb string, k model.Kind, ref model.ObjectRef) Access {
	a := Access{Verb: verb, Group: k.Group, Resource: k.Plural, Namespace: ref.Namespace, Name: ref.Name}
	if k.ClusterScoped {
		a.Namespace = ""
	}
	return a
}

// expected is the identity e's manifest claims: one object of e's resource
// under e's name. A namespace may carry its primary network, and each DRS
// file is one object of the DRS set.
func expected(e draft.Entry) func(after []object) error {
	res := e.Resource
	if res == "" {
		res = draft.ResourceVM
	}
	return func(after []object) error {
		refuse := fmt.Errorf("%w: the manifest must declare %s and nothing else", model.ErrInvalid, objectLabel(e))
		switch res {
		case draft.ResourceNamespace:
			return namespaceFile(after, e.Name, refuse)
		case draft.ResourceDRS:
			if len(after) != 1 || !slices.Contains(append(res.Kinds(), model.KindNS.Kind), after[0].ref.Kind) {
				return refuse
			}
			return nil
		}
		kinds := res.Kinds()
		if res == draft.ResourceVM {
			kinds = append(kinds, model.KindDV.Kind)
		}
		ns := model.ObjectNamespace(e.Namespace)
		// A library template's namespace is not where it lands: it is never applied.
		if len(after) != 1 || !slices.Contains(kinds, after[0].ref.Kind) || after[0].ref.Name != e.Name ||
			res != draft.ResourceTemplate && after[0].ref.Namespace != ns {
			return refuse
		}
		return nil
	}
}

// namespaceFile is a namespace's own file: the Namespace, and at most its
// primary network inside it.
func namespaceFile(after []object, name string, refuse error) error {
	namespaces, networks := 0, 0
	for _, o := range after {
		switch {
		case o.ref.Kind == model.KindNS.Kind && o.ref.Name == name:
			namespaces++
		case o.ref.Kind == model.KindUDN.Kind && o.ref.Namespace == name:
			networks++
		default:
			return refuse
		}
	}
	if namespaces != 1 || networks > 1 {
		return refuse
	}
	return nil
}

// object is one document of a manifest as Argo applies it. written is its
// identity as the document spells it; ref places a namespaced one that omits
// its namespace in its file's directory, as the declared index reads it.
type object struct {
	written model.ObjectRef
	ref     model.ObjectRef
	kind    model.Kind
	body    map[string]any
}

// argoObjects decodes content the way Argo's directory source does
// (gitops-engine's SplitYAML: apimachinery's YAML-or-JSON stream, each
// document converted to JSON) and refuses what Argo would apply differently
// from what dotvirt reads: a List, or any document with a top-level items
// list, which Argo unpacks into objects of their own; a file dotvirt's own
// parser splits or names otherwise; and a kind outside the managed table,
// for which no access review could be asked.
func argoObjects(path string, content []byte) ([]object, error) {
	var objs []object
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(content), 4096)
	for {
		var ext runtime.RawExtension
		err := dec.Decode(&ext)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("does not parse: %v", err)
		}
		raw := bytes.TrimSpace(ext.Raw)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("holds a document that is not an object")
		}
		o, err := objectOf(path, body)
		if err != nil {
			return nil, err
		}
		objs = append(objs, o)
	}
	heads, docs := manifest.Headers(content)
	if docs != len(objs) || len(heads) != len(objs) {
		return nil, fmt.Errorf("splits into documents differently for Argo and for dotvirt")
	}
	for i, h := range heads {
		if h != objs[i].written {
			return nil, fmt.Errorf("names %s %s differently for Argo and for dotvirt", h.Kind, h.Name)
		}
	}
	return objs, nil
}

// objectOf is one decoded document as an object of a managed kind.
func objectOf(path string, body map[string]any) (object, error) {
	if _, ok := body["items"]; ok {
		return object{}, fmt.Errorf("holds a document with a top-level items list, which Argo unpacks into objects of their own; declare each as its own document")
	}
	if key := foldedTwice(body); key != "" {
		return object{}, fmt.Errorf("holds a mapping with two keys spelled %q in different cases, which decoders read differently", key)
	}
	apiVersion, _ := body["apiVersion"].(string)
	kind, _ := body["kind"].(string)
	meta, _ := body["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	namespace, _ := meta["namespace"].(string)
	if strings.HasSuffix(kind, "List") {
		return object{}, fmt.Errorf("holds a %s, which Argo unpacks into objects of their own; declare each as its own document", kind)
	}
	if apiVersion == "" || kind == "" || name == "" {
		return object{}, fmt.Errorf("holds a document without apiVersion, kind or metadata.name")
	}
	if !apiVersionForm.MatchString(apiVersion) {
		return object{}, fmt.Errorf("declares %s %s under apiVersion %q, which is no group/version", kind, name, apiVersion)
	}
	group, _, ok := strings.Cut(apiVersion, "/")
	if !ok {
		group = ""
	}
	k, ok := managedKind(group, kind)
	if !ok {
		return object{}, fmt.Errorf("declares %s %s (%s), which dotvirt does not manage", kind, name, apiVersion)
	}
	o := object{written: model.ObjectRef{Kind: kind, Namespace: namespace, Name: name}, kind: k, body: body}
	if annotations, _ := meta["annotations"].(map[string]any); annotations != nil {
		if options, _ := annotations["argocd.argoproj.io/sync-options"].(string); strings.Contains(options, "Validate=false") {
			return object{}, fmt.Errorf("has %s %s skip the apiserver's validation (Validate=false)", kind, name)
		}
	}
	o.ref = o.written
	switch {
	case k.ClusterScoped:
		o.ref.Namespace = ""
	case namespace == "":
		o.ref.Namespace = defaultNamespace(path)
	}
	return o, nil
}

// foldedTwice is a key of a mapping anywhere in v that the mapping holds
// twice under case folding, "" when none: Go's JSON decoding matches fields
// case-insensitively, the apiserver's does not, so the two would read
// different values.
func foldedTwice(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k, child := range t {
			if slices.ContainsFunc(keys, func(seen string) bool { return strings.EqualFold(seen, k) }) {
				return k
			}
			keys = append(keys, k)
			if key := foldedTwice(child); key != "" {
				return key
			}
		}
	case []any:
		for _, child := range t {
			if key := foldedTwice(child); key != "" {
				return key
			}
		}
	}
	return ""
}

// apiVersionForm is an apiVersion as Kubernetes spells one: an optional DNS
// subdomain group, then a version (v1, v1beta1). It goes into a dry run's
// URL, so nothing else may.
var apiVersionForm = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?v[0-9]+((alpha|beta)[0-9]+)?$`)

// managedKind is the managed kind group serves under that name.
func managedKind(group, kind string) (model.Kind, bool) {
	if _, k, ok := model.LookupKind(kind); ok && k.Group == group {
		return k, true
	}
	if kind == model.KindDV.Kind && group == model.KindDV.Group {
		return model.KindDV, true
	}
	return model.Kind{}, false
}
