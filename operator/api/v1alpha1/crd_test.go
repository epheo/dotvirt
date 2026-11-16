package v1alpha1

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// OLM v0 validates every stored Dotvirt against a new CRD's schema before it
// updates the CRD, with no ratcheting: a new constraint on a field that
// already shipped fails the upgrade's InstallPlan for any install whose stored
// value it refuses. Constraints go on new fields only.
func TestShippedFieldsGainNoValidation(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "config", "crd", "bases", "dotvirt.io_dotvirts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := utilyaml.NewYAMLOrJSONDecoder(f, 4096).Decode(crd); err != nil {
		t.Fatal(err)
	}
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	url := spec.Properties["metrics"].Properties["url"]
	url.Description = ""
	if want := (apiextensionsv1.JSONSchemaProps{Type: "string"}); !reflect.DeepEqual(url, want) {
		t.Errorf("spec.metrics.url schema = %+v, want only type string, as shipped", url)
	}
}
