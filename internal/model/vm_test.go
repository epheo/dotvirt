package model

import "testing"

// A shown source never carries what fetches it privately: the query (a
// pre-signed URL's signature) and the userinfo go, and say so.
func TestImportFrom(t *testing.T) {
	for _, c := range []struct {
		kind, url string
		want      DiskImport
	}{
		{ImportRegistry, "docker://quay.io/containerdisks/fedora:42", DiskImport{Kind: ImportRegistry, Source: "quay.io/containerdisks/fedora:42"}},
		{ImportRegistry, "DOCKER://quay.io/containerdisks/fedora:42", DiskImport{Kind: ImportRegistry, Source: "quay.io/containerdisks/fedora:42"}},
		{ImportRegistry, "quay.io/containerdisks/fedora@sha256:0a1b", DiskImport{Kind: ImportRegistry, Source: "quay.io/containerdisks/fedora@sha256:0a1b"}},
		{ImportRegistry, "docker://fedora@sha256:0a1b", DiskImport{Kind: ImportRegistry, Source: "fedora@sha256:0a1b"}},
		{ImportRegistry, "docker://robot:s3cret@registry.example:5000/vm/rhel:9", DiskImport{Kind: ImportRegistry, Source: "registry.example:5000/vm/rhel:9", Redacted: true}},
		{ImportRegistry, "oci-archive://images/rhel.tar", DiskImport{Kind: ImportRegistry, Source: "oci-archive://images/rhel.tar"}},
		{ImportURL, "https://images.example.com/noble.img", DiskImport{Kind: ImportURL, Source: "https://images.example.com/noble.img"}},
		{ImportURL, " HTTP://files.lab/win.qcow2 ", DiskImport{Kind: ImportURL, Source: "http://files.lab/win.qcow2"}},
		{ImportURL, "https://bucket.s3.example/a.img?X-Amz-Signature=abc&X-Amz-Expires=60", DiskImport{Kind: ImportURL, Source: "https://bucket.s3.example/a.img", Redacted: true}},
		{ImportURL, "https://ops:hunter2@images.example.com/a.img", DiskImport{Kind: ImportURL, Source: "https://images.example.com/a.img", Redacted: true}},
		{ImportURL, "https://ops@images.example.com", DiskImport{Kind: ImportURL, Source: "https://images.example.com", Redacted: true}},
		{ImportURL, "https://images.example.com/a.img#part-2", DiskImport{Kind: ImportURL, Source: "https://images.example.com/a.img"}},
		{ImportURL, "https://images.example.com/a@b/c.img", DiskImport{Kind: ImportURL, Source: "https://images.example.com/a@b/c.img"}},
	} {
		if got := ImportFrom(c.kind, c.url); *got != c.want {
			t.Errorf("ImportFrom(%q, %q) = %+v, want %+v", c.kind, c.url, *got, c.want)
		}
	}
}
