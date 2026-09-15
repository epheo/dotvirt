package manifest

import (
	"reflect"

	"sigs.k8s.io/yaml"
)

// SameDocument compares two manifests as data, so key order and quoting do not
// count and every field does.
func SameDocument(a, b []byte) bool {
	var da, db any
	if yaml.Unmarshal(a, &da) != nil || yaml.Unmarshal(b, &db) != nil {
		return false
	}
	return reflect.DeepEqual(da, db)
}
