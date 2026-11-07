package vmtemplate

import (
	"strings"
	"testing"
)

func TestParseValidTemplate(t *testing.T) {
	tpl := Parse("templates/fedora-server.yaml", []byte(fedoraServer), "acme")
	if tpl.Error != "" {
		t.Fatalf("unexpected error: %s", tpl.Error)
	}
	if tpl.Name != "fedora-server" || tpl.Library != "acme" || tpl.SourceFile != "templates/fedora-server.yaml" {
		t.Fatalf("identity wrong: %+v", tpl)
	}
	if tpl.Description == "" || tpl.Instancetype != "u1.medium" || tpl.Preference != "fedora" {
		t.Fatalf("summary wrong: %+v", tpl)
	}
	if len(tpl.Parameters) != 5 || tpl.Parameters[0].Name != "NAME" || tpl.Parameters[0].Generate != "expression" {
		t.Fatalf("parameters wrong: %+v", tpl.Parameters)
	}
	// The image namespace is a parameter: its default resolves the reference.
	if tpl.Image != "openshift-virtualization-os-images/fedora" {
		t.Fatalf("image = %q", tpl.Image)
	}
}

// An image left to a parameter without a default, or to the deploy
// namespace, matches no catalog entry, so it stays empty.
func TestParseTemplateImageUnresolved(t *testing.T) {
	for _, content := range []string{
		strings.Replace(fedoraServer, "name: fedora\n              namespace:", "name: ${SSH_AUTHORIZED_KEY}\n              namespace:", 1),
		strings.Replace(fedoraServer, "              namespace: ${OS_IMAGE_NAMESPACE}\n", "", 1),
	} {
		if content == fedoraServer {
			t.Fatal("fixture edit did not apply")
		}
		if tpl := Parse("templates/t.yaml", []byte(content), "acme"); tpl.Image != "" {
			t.Errorf("image = %q, want empty", tpl.Image)
		}
	}
}

func TestParseBadFilesStillListed(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{"broken YAML", "{nope"},
		{"wrong kind", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tpl := Parse("templates/x.yaml", []byte(tc.content), "acme")
			if tpl.Error == "" {
				t.Fatal("want Error set")
			}
			if tpl.Name != "x" || tpl.YAML != tc.content {
				t.Fatalf("entry not listed intact: %+v", tpl)
			}
		})
	}
}

func TestParseParameterizedSummaryStaysEmpty(t *testing.T) {
	content := strings.Replace(fedoraServer, "name: u1.medium", "name: ${SIZE}", 1)
	tpl := Parse("templates/t.yaml", []byte(content), "acme")
	if tpl.Error != "" {
		t.Fatalf("unexpected error: %s", tpl.Error)
	}
	if tpl.Instancetype != "${SIZE}" {
		t.Fatalf("summary should carry the raw reference, got %q", tpl.Instancetype)
	}
}
