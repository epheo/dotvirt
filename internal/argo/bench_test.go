package argo

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// benchApp is one project Application managing 40 VMs and 10 networks, with a
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
// Applications: the cost of one Application write (here a reconcile that moved
// nothing, the steady state).
func BenchmarkDriftRebuild(b *testing.B) {
	s := NewSnapshot(&Client{}, nil)
	for i := range 50 {
		_ = s.apps.Add(benchApp(i))
	}
	s.synced.Mark()
	s.derive()
	b.ReportAllocs()
	for b.Loop() {
		s.derive()
	}
}

// BenchmarkReconcileWrite is one Application write that moves only the reconcile
// stamps: the store write plus the noise compare, and no derive.
func BenchmarkReconcileWrite(b *testing.B) {
	s := NewSnapshot(&Client{}, nil)
	st := s.store()
	var list []any
	for i := range 50 {
		list = append(list, benchApp(i))
	}
	_ = st.Replace(list, "1")
	<-s.dirty
	// Two distinct objects, so every compare walks two separate trees.
	var ticks [2]*unstructured.Unstructured
	for i := range ticks {
		ticks[i] = benchApp(7)
		_ = unstructured.SetNestedField(ticks[i].Object, fmt.Sprintf("t%d", i), "status", "reconciledAt")
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_ = st.Update(ticks[i%2])
	}
	if len(s.dirty) > 0 {
		b.Fatal("a reconcile-only write marked the view dirty")
	}
}
