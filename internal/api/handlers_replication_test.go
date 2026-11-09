package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/replication"
)

// replicationSource serves one Volume, its PersistentVolume, and a watch on
// Volumes a test can write through.
type replicationSource struct {
	mu      sync.Mutex
	volumes *watch.FakeWatcher
}

func (*replicationSource) HasAPIResource(schema.GroupVersionResource) bool { return true }

func (*replicationSource) Can(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

func replicatedVolume(disk string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "r-drbd.io/v1alpha1", "kind": "Volume",
		"spec": map[string]any{"sizeBytes": int64(1), "replicas": int64(1), "assignment": map[string]any{
			"replicas": []any{map[string]any{"node": "host-1", "role": "diskful"}},
		}},
		"status": map[string]any{"phase": "Ready", "replicas": []any{
			map[string]any{"node": "host-1", "present": true, "diskState": disk},
		}},
	}}
	u.SetName("pvc-1")
	return u
}

func (f *replicationSource) DynamicListWatch(gvr schema.GroupVersionResource) *cache.ListWatch {
	var items []unstructured.Unstructured
	switch gvr {
	case replication.GVRVolume:
		items = []unstructured.Unstructured{*replicatedVolume("UpToDate")}
	case replication.GVRPV:
		pv := unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "PersistentVolume",
			"spec": map[string]any{
				"csi":      map[string]any{"driver": "r-drbd.io", "volumeHandle": "pvc-1"},
				"claimRef": map[string]any{"namespace": "team-a-dev", "name": "root"},
			},
			"status": map[string]any{"phase": "Bound"},
		}}
		pv.SetName("pvc-1")
		items = []unstructured.Unstructured{pv}
	}
	return &cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			return &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List"}, Items: items}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			w := watch.NewFake()
			if gvr == replication.GVRVolume {
				f.mu.Lock()
				f.volumes = w
				f.mu.Unlock()
			}
			return w, nil
		},
	}
}

func (f *replicationSource) write(t *testing.T, o *unstructured.Unstructured) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f.mu.Lock()
		w := f.volumes
		f.mu.Unlock()
		if w != nil {
			w.Modify(o)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no watch on Volumes")
		}
	}
}

// replicationServer runs the snapshot over src and waits until the Volume is
// joined to its claim.
func replicationServer(t *testing.T, fc *fakeCluster, src *replicationSource) *Server {
	t.Helper()
	s := fc.server(nil, Config{})
	s.replication = replication.New(src, s.bus, func() bool { return true })
	s.replication.Run(t.Context())
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if len(s.replication.Protection([]string{"team-a-dev"})) == 1 {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("the snapshot never joined the Volume to its claim")
		}
	}
}

// replicationVersion is the frame's watermark for a caller who sees team-a-dev.
func replicationVersion(t *testing.T, s *Server, token string) uint64 {
	t.Helper()
	r := asUser(httptest.NewRequest(http.MethodGet, "/", nil), token)
	id, c, err := s.userCluster(r)
	if err != nil {
		t.Fatal(err)
	}
	return s.replicationWatermark(r.Context(), id, c, map[string]bool{"team-a-dev": true})
}

func TestReplicationPlacementIsAskedOfTheCaller(t *testing.T) {
	s := newFakeCluster(t).server(nil, Config{})
	s.replication = replication.New(nil, eventbus.New(), func() bool { return false })

	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/storage/replication", ""), http.StatusOK)
	rec := do(s, viewerToken, http.MethodGet, "/api/storage/replication", "")
	wantStatus(t, rec, http.StatusForbidden)
	if strings.Contains(rec.Body.String(), "host") {
		t.Fatalf("a refusal names placement: %s", rec.Body)
	}
}

func TestProtectionAnswersAnyCaller(t *testing.T) {
	s := newFakeCluster(t).server(nil, Config{})
	s.replication = replication.New(nil, eventbus.New(), func() bool { return false })

	rec := do(s, viewerToken, http.MethodGet, "/api/storage/protection", "")
	wantStatus(t, rec, http.StatusOK)
	var got []model.DiskProtection
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("protection answered null, want an empty list")
	}
}

func TestProtectionNeedsTheRightToListClaims(t *testing.T) {
	for _, c := range []struct {
		name   string
		claims bool
		rows   int
		moves  bool
	}{
		{"VMs and claims", true, 1, true},
		{"VMs, no claims", false, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.readsVMs = tenantReadsTeamA
			fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
				return token == tenantToken && c.claims && a.Verb == "list" &&
					a.Resource == "persistentvolumeclaims" && a.Namespace == "team-a-dev"
			}
			src := &replicationSource{}
			s := replicationServer(t, fc, src)

			rec := do(s, tenantToken, http.MethodGet, "/api/storage/protection", "")
			wantStatus(t, rec, http.StatusOK)
			var got []model.DiskProtection
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != c.rows {
				t.Fatalf("rows = %+v, want %d", got, c.rows)
			}

			before := replicationVersion(t, s, tenantToken)
			gen := s.replication.Gen("team-a-dev")
			src.write(t, replicatedVolume("Outdated"))
			for deadline := time.Now().Add(5 * time.Second); s.replication.Gen("team-a-dev") == gen; time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("the write never reached the snapshot")
				}
			}
			if after := replicationVersion(t, s, tenantToken); (after != before) != c.moves {
				t.Fatalf("version %d then %d, want moved=%v", before, after, c.moves)
			}
		})
	}
}
