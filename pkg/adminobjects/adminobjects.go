// Package adminobjects is what an install needs but its operator may not
// create: objects the operator renders and reports in its status, and a
// cluster admin installs from the console, under the admin's own token. The
// operator only reads them back to report whether they are in place.
//
// Allowed is the whole list of what an operator may ask for. The console
// applies nothing else, whatever a status lists: an operator that could have
// any object applied by an admin could have itself granted anything.
package adminobjects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RenderAnnotation carries a digest of the render an object came from, so a
// reader tells a copy rendered for other parameters or another version from
// the current one.
const RenderAnnotation = "dotvirt.io/render"

// State is how a live object compares with its render.
type State string

const (
	NotInstalled State = "NotInstalled"
	// Outdated: rendered for other parameters or by another version, or
	// changed after it was installed.
	Outdated  State = "Outdated"
	Installed State = "Installed"
)

// Object is one rendered object and what it is for.
type Object struct {
	Purpose string
	Object  *unstructured.Unstructured
}

// Ref names one object an install may ask an admin to apply.
type Ref struct {
	Group, Version, Kind, Resource string
	// Namespace is "" for a cluster-scoped kind.
	Namespace, Name string
}

// GVR is the resource the object is applied as.
func (r Ref) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
}

// Allowed is every object an install in installNS may ask a cluster admin to
// apply. Append to it to ask for more, with a comparer in same.
func Allowed(installNS string) []Ref {
	var out []Ref
	for _, name := range GuardNames {
		out = append(out,
			Ref{"admissionregistration.k8s.io", "v1", "ValidatingAdmissionPolicy", "validatingadmissionpolicies", "", name},
			Ref{"admissionregistration.k8s.io", "v1", "ValidatingAdmissionPolicyBinding", "validatingadmissionpolicybindings", "", name})
	}
	return out
}

// Allow is the entry of allowed o is, by group, version, kind, namespace and
// name, and whether there is one.
func Allow(allowed []Ref, o *unstructured.Unstructured) (Ref, bool) {
	gv, err := schema.ParseGroupVersion(o.GetAPIVersion())
	if err != nil {
		return Ref{}, false
	}
	for _, r := range allowed {
		if r.Group == gv.Group && r.Version == gv.Version && r.Kind == o.GetKind() && r.Namespace == o.GetNamespace() && r.Name == o.GetName() {
			return r, true
		}
	}
	return Ref{}, false
}

// same compares what decides an object's effect, per kind, ignoring apiserver
// defaults. A kind without one is compared by its render digest alone.
var same = map[schema.GroupKind]func(want, live *unstructured.Unstructured) bool{
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicy"}:        samePolicy,
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicyBinding"}: sameBinding,
}

// Compare weighs live, nil when absent, against want. detail says how they
// differ.
func Compare(want, live *unstructured.Unstructured) (state State, detail string) {
	what := want.GetKind() + " " + want.GetName()
	if live == nil {
		return NotInstalled, what + " is missing"
	}
	if live.GetAnnotations()[RenderAnnotation] != want.GetAnnotations()[RenderAnnotation] {
		return Outdated, what + " was rendered for another install or version"
	}
	if eq := same[want.GroupVersionKind().GroupKind()]; eq != nil && !eq(want, live) {
		return Outdated, what + " was changed after it was installed"
	}
	return Installed, ""
}

// stamp sets RenderAnnotation to a digest of o's content.
func stamp(o *unstructured.Unstructured) {
	c := o.DeepCopy()
	unstructured.RemoveNestedField(c.Object, "metadata")
	unstructured.RemoveNestedField(c.Object, "status")
	b, _ := json.Marshal([]any{o.GetNamespace(), o.GetName(), c.Object})
	sum := sha256.Sum256(b)
	a := o.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[RenderAnnotation] = hex.EncodeToString(sum[:8])
	o.SetAnnotations(a)
}

// ApplyCommand is a one-line server-side apply of objs, for an admin without
// the console.
func ApplyCommand(objs []Object) (string, error) {
	items := make([]any, len(objs))
	for i, o := range objs {
		items[i] = o.Object.Object
	}
	b, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	if err != nil {
		return "", err
	}
	if strings.Contains(string(b), "'") {
		return "", errors.New("an admin object holds a single quote")
	}
	return "echo '" + string(b) + "' | kubectl apply --server-side --field-manager=dotvirt --force-conflicts -f -", nil
}
