package vmtemplate

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// Goldens pin the rendered output of templates already committed to project
// repos, so an engine bump that changes what an existing template deploys fails
// here. Regenerate with -update only when that change is intended.
var update = flag.Bool("update", false, "rewrite golden files")

func TestRenderGolden(t *testing.T) {
	derived, err := Derive([]byte(sourceVM), "web-template", "golden web tier")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	typed, err := os.ReadFile("testdata/golden/typed.tpl.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		raw    []byte
		params map[string]string
	}{
		{"fedora-server-defaults", []byte(fedoraServer), map[string]string{"NAME": "web-01"}},
		{"fedora-server-custom", []byte(fedoraServer), map[string]string{
			"NAME": "db-01", "HOSTNAME": "db", "CLOUD_USER": "ops",
			"SSH_AUTHORIZED_KEY": "ssh-ed25519 AAAAC3Nza ops@x", "OS_IMAGE_NAMESPACE": "images",
		}},
		{"fedora-minimal", []byte(fedoraMinimal), map[string]string{"NAME": "min-01"}},
		{"derived", derived, map[string]string{"NAME": "web-02"}},
		{"typed", typed, map[string]string{"NAME": "typed-01", "CORES": "4"}},
		{"legacy", []byte(testTemplate), map[string]string{"NAME": "vm-01", "PASSWORD": "s3cret"}},
		{"err-required-missing", []byte(testTemplate), map[string]string{"NAME": "vm-01"}},
		{"err-unknown-param", []byte(testTemplate), map[string]string{"PASSWORD": "x", "NOPE": "y"}},
		{"err-not-json", typed, map[string]string{"NAME": "t", "CORES": "two"}},
		{"err-unknown-vm-field", []byte(fedoraMinimal + "      bogusField: 1\n"), map[string]string{"NAME": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := EngineRenderer{}.Render(tc.raw, tc.params, "tenant-a")
			got := "error: <nil>\n"
			if err != nil {
				got = "error: " + err.Error() + "\n"
			} else {
				got = "name: " + r.Name + "\nnamespace: " + r.Namespace + "\nmessage: " + r.Message +
					"\n---\n" + string(r.Manifest)
			}
			checkGolden(t, "render-"+tc.name, []byte(got))
		})
	}
}

func TestParseGolden(t *testing.T) {
	derived, err := Derive([]byte(sourceVM), "web-template", "golden web tier")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	typed, err := os.ReadFile("testdata/golden/typed.tpl.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"fedora-server":  []byte(fedoraServer),
		"fedora-minimal": []byte(fedoraMinimal),
		"derived":        derived,
		"typed":          typed,
	} {
		t.Run(name, func(t *testing.T) {
			tpl := Parse("templates/"+name+".yaml", raw, "acme")
			tpl.YAML = ""
			out, err := json.MarshalIndent(tpl, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, "parse-"+name, append(out, '\n'))
		})
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from golden:\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}
