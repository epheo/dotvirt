package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

func TestDNS1123Name(t *testing.T) {
	// Rejected: traversal, separators, uppercase, bad edges, spaces, over-length.
	bad := []string{"", "../x", "a/b", "..", "Team-A", "-x", "x-", "a b", strings.Repeat("a", 64)}
	for _, s := range bad {
		if DNS1123Name(s) {
			t.Errorf("DNS1123Name(%q) = true, want false", s)
		}
	}
	if DNS1123Name("x..y") {
		t.Error(`DNS1123Name("x..y") = true, want false`)
	}
	// Accepted: DNS-1123 labels, including the 63-char maximum.
	for _, s := range []string{"team-a", "a", "x1", "abc-123", strings.Repeat("a", 63)} {
		if !DNS1123Name(s) {
			t.Errorf("DNS1123Name(%q) = false, want true", s)
		}
	}
}

func TestRequireDNS1123(t *testing.T) {
	if err := RequireDNS1123("name", "ok-name"); err != nil {
		t.Errorf("RequireDNS1123 valid: %v", err)
	}
	err := RequireDNS1123("VM name", "../evil")
	if err == nil || !strings.Contains(err.Error(), "VM name") {
		t.Errorf("RequireDNS1123 invalid: %v", err)
	}
}

func TestRepoPath(t *testing.T) {
	for _, ok := range []string{"vms/web.yaml", "web.yaml", "a/b/c.yaml"} {
		if !RepoPath(ok) {
			t.Errorf("RepoPath(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../secrets.yaml", "a/../b.yaml", "./a.yaml", "a//b.yaml", "a/b/", `a\b.yaml`, "a/./b.yaml"} {
		if RepoPath(bad) {
			t.Errorf("RepoPath(%q) = true, want false", bad)
		}
	}
}

func TestRequireLabels(t *testing.T) {
	ok := map[string]string{"app": "web", "example.com/tier": "", "env": "true", "v": "1.0"}
	if err := RequireLabels("label", ok); err != nil {
		t.Errorf("RequireLabels(%v): %v", ok, err)
	}
	for _, bad := range []map[string]string{
		{"app": "a\n---\nkind: RoleBinding"},
		{"app": "has space"},
		{"bad key": "x"},
		{"/x": "x"},
		{"a/b/c": "x"},
		{"app": strings.Repeat("a", 64)},
	} {
		if err := RequireLabels("label", bad); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("RequireLabels(%q): want ErrInvalid, got %v", bad, err)
		}
	}
}

func TestRequireQuantity(t *testing.T) {
	for _, ok := range []string{"4Gi", "512Mi", "1", "1e3"} {
		if err := RequireQuantity("memory", ok); err != nil {
			t.Errorf("RequireQuantity(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "-1Gi", "4 Gi", "lots", "4Gi\n"} {
		if err := RequireQuantity("memory", bad); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("RequireQuantity(%q): want ErrInvalid, got %v", bad, err)
		}
	}
}

func TestRequireObjectNameAndLists(t *testing.T) {
	if err := RequireObjectName("instance type", "u1.medium"); err != nil {
		t.Errorf("RequireObjectName: %v", err)
	}
	for _, bad := range []string{"", "U1", "a b", "a\nb", "-x"} {
		if err := RequireObjectName("instance type", bad); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("RequireObjectName(%q): want ErrInvalid, got %v", bad, err)
		}
	}
	if err := RequireDNS1123List("namespace", []string{"a", "b-1"}); err != nil {
		t.Errorf("RequireDNS1123List: %v", err)
	}
	if err := RequireDNS1123List("namespace", []string{"a", "B"}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("RequireDNS1123List: want ErrInvalid, got %v", err)
	}
	if err := RequireOneOf("power", "On", "On", "Off"); err != nil {
		t.Errorf("RequireOneOf: %v", err)
	}
	if err := RequireOneOf("power", "on", "On", "Off"); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("RequireOneOf: want ErrInvalid, got %v", err)
	}
}

// An import source is fetched by the cluster as given: http or https only, a
// registry reference with docker:// optional, never credentials, spaces or
// control characters, and a refusal never quotes it back.
func TestImportSource(t *testing.T) {
	for _, c := range []struct{ kind, in, want string }{
		{model.ImportURL, "https://images.example/noble.img", "https://images.example/noble.img"},
		{model.ImportURL, "http://files.lab:8080/win.qcow2?X-Amz-Signature=abc", "http://files.lab:8080/win.qcow2?X-Amz-Signature=abc"},
		{model.ImportURL, "HTTPS://images.example/a.img", "HTTPS://images.example/a.img"},
		{model.ImportRegistry, "quay.io/containerdisks/fedora:42", "docker://quay.io/containerdisks/fedora:42"},
		{model.ImportRegistry, "docker://quay.io/containerdisks/fedora:42", "docker://quay.io/containerdisks/fedora:42"},
		{model.ImportRegistry, "Docker://registry.example:5000/vm/rhel:9", "docker://registry.example:5000/vm/rhel:9"},
		{model.ImportRegistry, "fedora", "docker://fedora"},
		{model.ImportRegistry, "quay.io/containerdisks/fedora@sha256:" + strings.Repeat("ab", 32), "docker://quay.io/containerdisks/fedora@sha256:" + strings.Repeat("ab", 32)},
	} {
		got, err := ImportSource(c.kind, c.in)
		if err != nil || got != c.want {
			t.Errorf("ImportSource(%q, %q) = %q, %v; want %q", c.kind, c.in, got, err, c.want)
		}
	}
	for _, c := range []struct{ kind, in string }{
		{model.ImportURL, ""},
		{model.ImportURL, "ftp://files.lab/a.img"},
		{model.ImportURL, "file:///etc/passwd"},
		{model.ImportURL, "images.example/noble.img"},
		{model.ImportURL, "https:///noble.img"},
		{model.ImportURL, "https://ops:s3cret@images.example/a.img?sig=xyz"},
		{model.ImportURL, "https://images.example/a b.img"},
		{model.ImportURL, " https://images.example/a.img"},
		{model.ImportURL, "https://images.example/a.img\n"},
		{model.ImportURL, "https://images.example/a\x00.img"},
		{model.ImportRegistry, ""},
		{model.ImportRegistry, "docker://"},
		{model.ImportRegistry, "oci-archive://images/rhel.tar"},
		{model.ImportRegistry, "https://quay.io/containerdisks/fedora:42"},
		{model.ImportRegistry, "robot:pw@quay.io/vm/rhel:9"},
		{model.ImportRegistry, "quay.io/ContainerDisks/Fedora"},
		{model.ImportRegistry, "quay.io/containerdisks/fedora:42\t"},
		{"s3", "https://bucket.example/a.img"},
	} {
		_, err := ImportSource(c.kind, c.in)
		if !errors.Is(err, model.ErrInvalid) {
			t.Errorf("ImportSource(%q, %q): want ErrInvalid, got %v", c.kind, c.in, err)
		}
		if c.in != "" && err != nil && strings.Contains(err.Error(), c.in) && c.kind != "s3" {
			t.Errorf("ImportSource(%q, %q): the refusal quotes the source: %v", c.kind, c.in, err)
		}
	}
}
