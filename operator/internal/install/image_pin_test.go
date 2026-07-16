package install

import (
	"os"
	"strings"
	"testing"
)

// relatedImageValue scans a manifest for `name: <name>` and returns the `value:`
// on the following line. A line scan (not a YAML parse) keeps the test free of a
// yaml dependency while staying indentation-agnostic.
func relatedImageValue(t *testing.T, path, name string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.TrimPrefix(strings.TrimSpace(l), "- ") != "name: "+name {
			continue
		}
		if i+1 < len(lines) {
			if after, ok := strings.CutPrefix(strings.TrimSpace(lines[i+1]), "value:"); ok {
				return strings.TrimSpace(after)
			}
		}
		t.Fatalf("%s: %s has no value on the following line", path, name)
	}
	t.Fatalf("%s: env %s not found", path, name)
	return ""
}

// The release script (hack/release.sh) re-pins the operand digest in three
// places: this package's DefaultImage, the CSV's RELATED_IMAGE_DOTVIRT, and the
// manager manifest. A missed pin makes OLM installs and `make run` deploy
// DIFFERENT operands for the same operator version: assert all three agree.
func TestDefaultImagePinConsistency(t *testing.T) {
	for _, path := range []string{
		"../../bundle/manifests/dotvirt-operator.clusterserviceversion.yaml",
		"../../config/manager/manager.yaml",
	} {
		if got := relatedImageValue(t, path, "RELATED_IMAGE_DOTVIRT"); got != DefaultImage {
			t.Errorf("%s: RELATED_IMAGE_DOTVIRT = %q, want DefaultImage %q", path, got, DefaultImage)
		}
	}
}
