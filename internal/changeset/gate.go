package changeset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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
// access review asks it. Name is empty for a create: the apiserver's own
// check of a create does not name the object either.
type Access struct {
	Verb, Group, Resource, Namespace, Name string
}

func (a Access) String() string {
	what := a.Resource
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
// make the cluster do what id may not; proj's base branch is what they land on.
func (c *Coordinator) gate(id auth.Identity, proj project.ProjectInfo, entries []draft.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	read, err := c.read(proj)
	if err != nil {
		return err
	}
	var need []Access
	for _, e := range entries {
		a, err := c.entryAccess(read, proj, e)
		if err != nil {
			return err
		}
		need = append(need, a...)
	}
	return c.authorize(id, need)
}

// entryAccess is what committing e does to the cluster.
func (c *Coordinator) entryAccess(read *git.Repo, proj project.ProjectInfo, e draft.Entry) ([]Access, error) {
	switch {
	case fieldEdit(e):
		return []Access{access(verbUpdate, model.KindVM, model.ObjectRef{Namespace: e.Namespace, Name: e.Name})}, nil
	case e.Kind == draft.KindDelete:
		before, ok, err := read.LookupOnBranch(c.baseBranch, e.SourceFile)
		if err != nil || !ok {
			return nil, err
		}
		return fileAccess(proj, fileChange{path: e.SourceFile, before: before, remove: true}, nil)
	case e.Manifest != "":
		before, ok, err := read.LookupOnBranch(c.baseBranch, e.SourceFile)
		if err != nil {
			return nil, err
		}
		if !ok {
			before = nil
		}
		return fileAccess(proj, fileChange{path: e.SourceFile, before: before, after: []byte(e.Manifest)}, expected(e))
	}
	return nil, nil
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
// makes, by comparing it with what base holds there: an object only after
// declares is created, one both declare is updated when it differs, one only
// before declares is deleted. want, when set, is the identity the write
// claims; a revert claims none and may restore any managed object.
func fileAccess(proj project.ProjectInfo, ch fileChange, want func(after, before []object) error) ([]Access, error) {
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
	for _, o := range after {
		if !o.kind.ClusterScoped && o.written.Namespace == "" {
			return nil, fmt.Errorf("%w: %s %s must set metadata.namespace: Argo would place it in its Application's namespace", model.ErrInvalid, o.ref.Kind, o.ref.Name)
		}
	}
	if want != nil {
		if err := want(after, before); err != nil {
			return nil, err
		}
	}
	was := make(map[model.ObjectRef]object, len(before))
	for _, o := range before {
		was[o.ref] = o
	}
	var need []Access
	for _, o := range after {
		prev, had := was[o.ref]
		delete(was, o.ref)
		verb := verbCreate
		if had {
			if reflect.DeepEqual(prev.body, o.body) {
				continue
			}
			verb = verbUpdate
		}
		need = append(need, access(verb, o.kind, o.ref))
		if o.kind == model.KindRB {
			bind, err := bindAccess(o)
			if err != nil {
				return nil, err
			}
			need = append(need, bind)
		}
	}
	for _, o := range before {
		if _, gone := was[o.ref]; gone {
			need = append(need, access(verbDelete, o.kind, o.ref))
		}
	}
	return need, nil
}

// libraryAccess gates a write under templates/: blueprints Argo never
// applies, so they carry no namespace and touch no object. The shared library
// is the platform repo's, written with the right to create templates
// cluster-wide, as its routes ask; a project's library is gated by the
// project's authoring right at its routes and at propose.
func libraryAccess(proj project.ProjectInfo, ch fileChange, before, after []object, want func(after, before []object) error) ([]Access, error) {
	for _, o := range append(before, after...) {
		if o.kind != model.KindTemplate {
			return nil, fmt.Errorf("%w: %s holds a %s; the template library holds only %s", model.ErrInvalid, ch.path, o.ref.Kind, model.KindTemplate.Kind)
		}
	}
	if want != nil {
		if err := want(after, before); err != nil {
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
	if verb == verbCreate {
		a.Name = ""
	}
	return a
}

// bindAccess is the right a RoleBinding needs beyond its own create or
// update: binding the role it references, in its namespace, as the
// apiserver's escalation check asks it.
func bindAccess(rb object) (Access, error) {
	ref, _ := rb.body["roleRef"].(map[string]any)
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	resource := map[string]string{"ClusterRole": "clusterroles", "Role": "roles"}[kind]
	if resource == "" || name == "" {
		return Access{}, fmt.Errorf("%w: RoleBinding %s must reference a Role or ClusterRole by name", model.ErrInvalid, rb.ref.Name)
	}
	return Access{Verb: "bind", Group: model.KindRB.Group, Resource: resource, Namespace: rb.ref.Namespace, Name: name}, nil
}

// expected is the identity e's manifest claims: one object of e's resource
// under e's name, which an edit keeps at the kind base declares it as. A
// namespace may carry its primary network, and each DRS file is one object of
// the DRS set.
func expected(e draft.Entry) func(after, before []object) error {
	res := e.Resource
	if res == "" {
		res = draft.ResourceVM
	}
	return func(after, before []object) error {
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
		if e.Kind == draft.KindEdit {
			for _, o := range before {
				if o.ref.Namespace == ns && o.ref.Name == e.Name && slices.Contains(kinds, o.ref.Kind) {
					kinds = []string{o.ref.Kind}
				}
			}
		}
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
	group, _, ok := strings.Cut(apiVersion, "/")
	if !ok {
		group = ""
	}
	k, ok := managedKind(group, kind)
	if !ok {
		return object{}, fmt.Errorf("declares %s %s (%s), which dotvirt does not manage", kind, name, apiVersion)
	}
	o := object{written: model.ObjectRef{Kind: kind, Namespace: namespace, Name: name}, kind: k, body: body}
	o.ref = o.written
	switch {
	case k.ClusterScoped:
		o.ref.Namespace = ""
	case namespace == "":
		o.ref.Namespace = defaultNamespace(path)
	}
	return o, nil
}

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
