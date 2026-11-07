package argo

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// benchApp is one project Application managing 40 VMs and 10 segments, with a
// failed sync result over the same objects.
func benchApp(i int) *unstructured.Unstructured {
	ns := fmt.Sprintf("ns-%d", i)
	var resources, results []any
	for v := range 40 {
		resources = append(resources, vmResource(ns, fmt.Sprintf("vm-%d", v), "Synced", "Healthy"))
		results = append(results, map[string]any{
			"group": "kubevirt.io", "kind": "VirtualMachine", "namespace": ns,
			"name": fmt.Sprintf("vm-%d", v), "status": "Synced", "message": "unchanged",
		})
	}
	for u := range 10 {
		resources = append(resources, udnResource(ns, fmt.Sprintf("net-%d", u), "Synced", "Healthy"))
	}
	a := appWithSource("argocd", ns, fmt.Sprintf("https://forge/o/%s.git", ns), resources)
	status := a.Object["status"].(map[string]any)
	status["sync"] = map[string]any{"status": "Synced", "revision": "0123456789abcdef"}
	status["health"] = map[string]any{"status": "Healthy"}
	status["operationState"] = map[string]any{"phase": "Succeeded", "syncResult": map[string]any{"resources": results}}
	return a
}

// BenchmarkDriftRebuild is one derivation of the drift view over 50
// Applications.
func BenchmarkDriftRebuild(b *testing.B) {
	s := NewSnapshot(&Client{}, nil)
	for i := range 50 {
		_ = s.apps.Add(benchApp(i))
	}
	s.synced.Mark()
	b.ReportAllocs()
	for b.Loop() {
		s.driftDirty.Store(true)
		_ = s.Drift()
	}
}
