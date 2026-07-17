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
		"storage: 50Gi",                   // custom root disk size
		"runStrategy: Halted",             // not running
		"#cloud-config",                   // cloud-init
		"ssh-ed25519",                     // ssh key
		"name: full-data",                 // extra disk's blank DataVolume
		"blank: {}",                       // extra disk is a persistent blank DV, not emptyDisk
		"storage: 100Gi",                  // extra disk size
		"networkName: tenant-a/mt-bridge", // multus network
		"bridge:",                         // bridge iface for the NAD
		"app: full",                       // label
	} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest missing %q:\n%s", want, s)
		}
	}
}

// A VM may decline the primary NIC and live on secondary networks alone: no pod
// network, no masquerade, only the requested NAD-backed bridge interfaces.
func TestManifestSecondaryOnly(t *testing.T) {
	no := false
	_, content, err := Manifest(Spec{
		Name: "edge", Namespace: "team-c",
		Instancetype: "u1.small", Preference: "fedora",
		OSImage:        OSImageRef{Name: "fedora", Namespace: "kv"},
		PrimaryNetwork: &no,
		Networks:       []NetworkRef{{Name: "team-c/localnet-a"}},
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	s := string(content)
	if strings.Contains(s, "pod:") || strings.Contains(s, "masquerade:") {
		t.Errorf("primary declined but manifest still attaches the pod network:\n%s", s)
	}
	for _, want := range []string{"networkName: team-c/localnet-a", "bridge:"} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest missing %q:\n%s", want, s)
		}
	}
}

// nil PrimaryNetwork (the common case and any older stored spec) keeps the primary
// NIC attached — the default is unchanged.
func TestManifestPrimaryDefault(t *testing.T) {
	_, content, err := Manifest(Spec{
		Name: "web", Namespace: "team-a",
		Instancetype: "u1.medium", Preference: "fedora",
		OSImage: OSImageRef{Name: "fedora", Namespace: "kv"},
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if !strings.Contains(string(content), "masquerade:") {
		t.Errorf("primary NIC should attach by default:\n%s", content)
	}
}

// An extra disk is a persistent blank DataVolume; its storage class lands on the
// disk's own template, while an unset class omits the field (cluster default).
func TestManifestExtraDiskStorageClass(t *testing.T) {
	_, content, err := Manifest(Spec{
		Name: "vm1", Namespace: "ns1",
		Instancetype: "u1.small", Preference: "fedora",
		OSImage: OSImageRef{Name: "fedora", Namespace: "kv"},
		ExtraDisks: []ExtraDisk{
			{Name: "fast", Size: "20Gi", StorageClass: "lvms-vgfast"},
			{Name: "bulk", Size: "500Gi"},
		},
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	s := string(content)
	for _, want := range []string{
		"name: vm1-fast", "storageClassName: lvms-vgfast", // classed extra disk
		"name: vm1-bulk", "storage: 500Gi", // default-class extra disk
	} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest missing %q:\n%s", want, s)
		}
	}
	// The default-class disk must not inherit the other's class. Its template is
	// the last one; assert exactly one storageClassName appears in the document.
	if n := strings.Count(s, "storageClassName"); n != 1 {
		t.Errorf("want exactly one storageClassName (only the 'fast' disk), got %d:\n%s", n, s)
	}
}

func TestManifestValidation(t *testing.T) {
	_, _, err := Manifest(Spec{Name: "x"}) // missing required fields
	if err == nil {
		t.Fatal("expected validation error")
	}
}

// A chosen storage class lands on the root DV template; empty omits the field
// so the provisioner picks the cluster default.
func TestManifestStorageClass(t *testing.T) {
	base := Spec{
		Name: "vm1", Namespace: "ns1",
		Instancetype: "u1.small", Preference: "fedora",
		OSImage: OSImageRef{Name: "fedora", Namespace: "kv"},
	}

	withClass := base
	withClass.StorageClass = "lvms-vgfast"
	_, content, err := Manifest(withClass)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if !strings.Contains(string(content), "storageClassName: lvms-vgfast") {
		t.Errorf("manifest should pin the chosen class:\n%s", content)
	}

	_, content, err = Manifest(base)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if strings.Contains(string(content), "storageClassName") {
		t.Errorf("empty class must omit storageClassName (cluster default):\n%s", content)
	}
}

// The manifest lands in git: a typed password must never appear in it, only its
// crypt(3) hash; an already-hashed value must survive unchanged.
func TestManifestPasswordHashed(t *testing.T) {
	spec := Spec{
		Name: "pw", Namespace: "team-a",
		Instancetype: "u1.small", Preference: "fedora",
		OSImage:   OSImageRef{Name: "fedora", Namespace: "kv"},
		CloudInit: &CloudInit{User: "admin", Password: "hunter2-plaintext"},
	}
	_, content, err := Manifest(spec)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	s := string(content)
	if strings.Contains(s, "hunter2-plaintext") {
		t.Fatalf("plaintext password leaked into the manifest:\n%s", s)
	}
	if !strings.Contains(s, "password: $2a$") {
		t.Errorf("expected a bcrypt hash in the manifest:\n%s", s)
	}
	if spec.CloudInit.Password != "hunter2-plaintext" {
		t.Error("Manifest mutated the caller's spec")
	}

	const preHashed = "$6$rounds=4096$salt$abcdef"
	spec.CloudInit = &CloudInit{User: "admin", Password: preHashed}
	_, content, err = Manifest(spec)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if !strings.Contains(string(content), "password: "+preHashed) {
		t.Errorf("pre-hashed password must pass through unchanged:\n%s", content)
	}
}
