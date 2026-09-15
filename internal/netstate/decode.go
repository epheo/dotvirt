package netstate

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// decode converts a live object into its document shape. A conversion error
// is not fatal: the apiserver validated the object, so a field that fails to
// convert is near impossible, and the fields converted before it still keep
// the row visible rather than dropping a possibly-applying object.
func decode[T any](u *unstructured.Unstructured) T {
	var doc T
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc)
	return doc
}
