package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/netstate"
)

// listJSON renders a namespace list in name order: the list itself follows
// map order, which no reader depends on.
func listJSON(t *testing.T, l []netstate.NamespaceLabels) string {
	t.Helper()
	l = slices.Clone(l)
	slices.SortFunc(l, func(a, b netstate.NamespaceLabels) int { return strings.Compare(a.Name, b.Name) })
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// One namespace list serves every redaction while the snapshot's namespaces
// hold still, and after each move answers as a server never asked does.
func TestNamespaceLabelsFollowTheSnapshot(t *testing.T) {
	bus := eventbus.New()
	st, kube, _ := watchedState(t, bus, projectNS("tenant-a"), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
	s := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
	first := s.namespaceLabels()
	if again := s.namespaceLabels(); len(again) == 0 || &again[0] != &first[0] {
		t.Error("a snapshot that held still rebuilt the list")
	}

	ctx := context.Background()
	namespaces := kube.CoreV1().Namespaces()
	relabeled := projectNS("tenant-a")
	relabeled.Labels["env"] = "prod"
	for _, m := range []struct {
		name  string
		do    func() error
		until string
	}{
		{"project namespace added", func() error {
			_, err := namespaces.Create(ctx, projectNS("tenant-b"), metav1.CreateOptions{})
			return err
		}, `"dotvirt.io/project":"tenant-b"`},
		{"project namespace relabeled", func() error { _, err := namespaces.Update(ctx, relabeled, metav1.UpdateOptions{}); return err }, `"env":"prod"`},
		{"other namespace added", func() error {
			_, err := namespaces.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-public"}}, metav1.CreateOptions{})
			return err
		}, `"kube-public"`},
		{"project namespace deleted", func() error { return namespaces.Delete(ctx, "tenant-b", metav1.DeleteOptions{}) }, "!tenant-b"},
	} {
		if err := m.do(); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		waitUntil(t, m.name, func() bool {
			got := listJSON(t, s.namespaceLabels())
			if gone, ok := strings.CutPrefix(m.until, "!"); ok {
				return !strings.Contains(got, gone)
			}
			return strings.Contains(got, m.until)
		})
		fresh := NewServer(Deps{State: st, Bus: bus, Reader: &fakeDraft{}, Draft: &fakeDraft{}})
		if got, want := listJSON(t, s.namespaceLabels()), listJSON(t, fresh.namespaceLabels()); got != want {
			t.Errorf("after %s: memoized list differs from a fresh build:\n got %s\nwant %s", m.name, got, want)
		}
	}
}
