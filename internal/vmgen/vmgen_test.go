package vmgen

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestManifestMinimal(t *testing.T) {
	path, content, err := Manifest(Spec{
		Name:         "web",
		Namespace:    "team-a",
		Instancetype: "u1.medium",
		Preference:   "fedora",
		OSImage:      OSImageRef{Name: "fedora", Namespace: "openshift-virtualization-os-images"},
		Running:      true,
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if path != "team-a/web.yaml" {
		t.Errorf("path = %q", path)
	}

	// Parses back as a valid object with the expected key fields.
	var vm map[string]any
	if err := yaml.Unmarshal(content, &vm); err != nil {
		t.Fatalf("generated manifest invalid YAML: %v\n%s", err, content)
	}
	spec := vm["spec"].(map[string]any)
	if spec["runStrategy"] != "Always" {
		t.Errorf("runStrategy = %v", spec["runStrategy"])
	}
	if it := spec["instancetype"].(map[string]any); it["name"] != "u1.medium" {
		t.Errorf("instancetype = %v", it)
	}
	if pref := spec["preference"].(map[string]any); pref["name"] != "fedora" {
		t.Errorf("preference = %v", pref)
	}
	if !strings.Contains(string(content), "kind: DataSource") {
		t.Error("missing DataSource sourceRef")
	}
}

func TestManifestFull(t *testing.T) {
	_, content, err := Manifest(Spec{
		Name:         "full",
		Namespace:    "team-b",
		Instancetype: "u1.large",
		Preference:   "rhel.9",
		OSImage:      OSImageRef{Name: "rhel9", Namespace: "openshift-virtualization-os-images"},
		DiskSize:     "50Gi",
		Running:      false,
		CloudInit:    &CloudInit{User: "cloud-user", SSHKey: "ssh-ed25519 AAAA..."},
		ExtraDisks:   []ExtraDisk{{Name: "data", Size: "100Gi"}},
		Networks:     []NetworkRef{{Name: "tenant-a/mt-bridge"}},
		Labels:       map[string]string{"app": "full", "team": "b"},
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	s := string(content)
	for _, want := range []string{
		"storage: 50Gi",                   // custom disk size
		"runStrategy: Halted",             // not running
		"#cloud-config",                   // cloud-init
		"ssh-ed25519",                     // ssh key
		"emptyDisk",                       // extra disk
		"capacity: 100Gi",                 // extra disk size
		"networkName: tenant-a/mt-bridge", // multus network
		"bridge:",                         // bridge iface for the NAD
		"app: full",                       // label
	} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest missing %q:\n%s", want, s)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	_, _, err := Manifest(Spec{Name: "x"}) // missing required fields
	if err == nil {
		t.Fatal("expected validation error")
	}
}
