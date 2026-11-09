package api

import (
	"context"
	"encoding/json"
	"io"
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
	listed  *unstructured.Unstructured // the Volume served; nil is one in sync
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
		if f.listed != nil {
			items = []unstructured.Unstructured{*f.listed}
		}
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

func twoCopies(splitBrain bool) *unstructured.Unstructured {
	status := map[string]any{"phase": "Ready", "replicas": []any{
		map[string]any{"node": "host-1", "present": true, "diskState": "UpToDate"},
		map[string]any{"node": "host-2", "present": true, "diskState": "UpToDate"},
	}}
	if splitBrain {
		status["conditions"] = []any{map[string]any{"type": "SplitBrain", "status": "True"}}
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "r-drbd.io/v1alpha1", "kind": "Volume",
		"spec": map[string]any{"sizeBytes": int64(1), "replicas": int64(2), "assignment": map[string]any{
			"replicas": []any{
				map[string]any{"node": "host-1", "role": "diskful"},
				map[string]any{"node": "host-2", "role": "diskful"},
			},
		}},
		"status": status,
	}}
	u.SetName("pvc-1")
	return u
}

const volumePath = "/apis/r-drbd.io/v1alpha1/volumes/pvc-1"

// patchRecorder answers the caller's patch and keeps its body and token.
func patchRecorder(fc *fakeCluster) (bodies *[]string, tokens *[]string) {
	var b, t []string
	var mu sync.Mutex
	fc.on(http.MethodPatch, volumePath, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		b, t = append(b, string(data)), append(t, bearer(r))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"r-drbd.io/v1alpha1","kind":"Volume","metadata":{"name":"pvc-1"}}`))
	})
	return &b, &t
}

const splitAt = "2026-11-09T10:00:00Z"

// liveVolume is the Volume a resolve reads under the caller's token.
func liveVolume(split string, survivor string) map[string]any {
	spec := map[string]any{"assignment": map[string]any{"secret": "s3cret", "replicas": []any{
		map[string]any{"node": "host-1", "role": "diskful"},
		map[string]any{"node": "host-2", "role": "diskful"},
		map[string]any{"node": "host-3", "role": "tiebreaker"},
	}}}
	if survivor != "" {
		spec["resolve"] = map[string]any{"survivor": survivor}
	}
	status := map[string]any{}
	if split != "" {
		status["conditions"] = []any{map[string]any{"type": "SplitBrain", "status": "True", "lastTransitionTime": split}}
	}
	return map[string]any{
		"apiVersion": "r-drbd.io/v1alpha1", "kind": "Volume",
		"metadata": map[string]any{"name": "pvc-1", "resourceVersion": "42"},
		"spec":     spec, "status": status,
	}
}

func serveLive(fc *fakeCluster, v map[string]any) {
	fc.on(http.MethodGet, volumePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
}

func resolveBody(survivor, since string) string {
	return `{"survivor":"` + survivor + `","since":"` + since + `"}`
}

func TestResolveSplitBrainPatchesAsTheCaller(t *testing.T) {
	fc := newFakeCluster(t)
	serveLive(fc, liveVolume(splitAt, ""))
	bodies, tokens := patchRecorder(fc)
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})

	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/storage/replication/volumes/pvc-1/resolve", resolveBody("host-2", splitAt)), http.StatusNoContent)
	if len(*bodies) != 1 {
		t.Fatalf("patches = %q", *bodies)
	}
	var patch struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Resolve struct {
				Survivor string `json:"survivor"`
			} `json:"resolve"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte((*bodies)[0]), &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Metadata.ResourceVersion != "42" || patch.Spec.Resolve.Survivor != "host-2" {
		t.Fatalf("patch = %s", (*bodies)[0])
	}
	if (*tokens)[0] != adminToken {
		t.Fatalf("patched as %q, want the caller", (*tokens)[0])
	}
}

// A retried click names the survivor already named: nothing changes.
func TestResolveRetryWithTheSameSurvivorIsAllowed(t *testing.T) {
	fc := newFakeCluster(t)
	serveLive(fc, liveVolume(splitAt, "host-2"))
	patchRecorder(fc)
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/storage/replication/volumes/pvc-1/resolve", resolveBody("host-2", splitAt)), http.StatusNoContent)
}

func TestResolveRefusesWhatWouldDiscardTheWrongData(t *testing.T) {
	for _, c := range []struct {
		name     string
		live     map[string]any
		survivor string
		since    string
	}{
		{"no split brain any more", liveVolume("", ""), "host-1", splitAt},
		{"a newer split than the one decided on", liveVolume("2026-11-09T11:00:00Z", ""), "host-1", splitAt},
		{"a resolve already running for the other copy", liveVolume(splitAt, "host-1"), "host-2", splitAt},
		{"a host holding no copy", liveVolume(splitAt, ""), "host-9", splitAt},
		{"a witness holds no data to keep", liveVolume(splitAt, ""), "host-3", splitAt},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			serveLive(fc, c.live)
			bodies, _ := patchRecorder(fc)
			s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})
			wantStatus(t, do(s, adminToken, http.MethodPost, "/api/storage/replication/volumes/pvc-1/resolve", resolveBody(c.survivor, c.since)), http.StatusConflict)
			if len(*bodies) != 0 {
				t.Fatalf("patched anyway: %q", *bodies)
			}
		})
	}
}

func TestResolveNeedsTheSplitItAnswers(t *testing.T) {
	fc := newFakeCluster(t)
	bodies, _ := patchRecorder(fc)
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/storage/replication/volumes/pvc-1/resolve", `{"survivor":"host-1"}`), http.StatusBadRequest)
	if len(*bodies) != 0 {
		t.Fatalf("patched anyway: %q", *bodies)
	}
}

// Whatever lands between the read and the write loses the race to the patch.
func TestResolveReportsAConflictOnThePatch(t *testing.T) {
	fc := newFakeCluster(t)
	serveLive(fc, liveVolume(splitAt, ""))
	fc.on(http.MethodPatch, volumePath, func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict)
	})
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/storage/replication/volumes/pvc-1/resolve", resolveBody("host-1", splitAt)), http.StatusConflict)
}

func TestResolveOfAVolumeGoneIsNotFound(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodGet, volumePath, func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound)
	})
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/storage/replication/volumes/pvc-1/resolve", resolveBody("host-1", splitAt)), http.StatusNotFound)
}

func TestRepairsNeedPatchAndSayNothingOfWhatExists(t *testing.T) {
	fc := newFakeCluster(t)
	readOnly := "reader-token"
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == adminToken || (token == readOnly && a.Verb == "list")
	}
	bodies, _ := patchRecorder(fc)
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(true)})

	for _, token := range []string{readOnly, tenantToken} {
		for _, target := range []string{
			"/api/storage/replication/volumes/pvc-1/resolve",
			"/api/storage/replication/volumes/no-such/resolve",
			"/api/storage/replication/hosts/host-1/evict?dryRun=true",
		} {
			wantStatus(t, do(s, token, http.MethodPost, target, `{"survivor":"host-1"}`), http.StatusForbidden)
		}
	}
	if len(*bodies) != 0 {
		t.Fatalf("patched anyway: %q", *bodies)
	}
}

func TestEvictHostDryRunTouchesNothing(t *testing.T) {
	fc := newFakeCluster(t)
	bodies, _ := patchRecorder(fc)
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(false)})

	rec := do(s, adminToken, http.MethodPost, "/api/storage/replication/hosts/host-1/evict?dryRun=true", "")
	wantStatus(t, rec, http.StatusOK)
	var plan model.ReplicationEvict
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Moving) != 1 || plan.Moving[0] != "pvc-1" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(*bodies) != 0 {
		t.Fatalf("a dry run patched: %q", *bodies)
	}
}

func TestEvictHostPatchesEachVolumeAsTheCaller(t *testing.T) {
	fc := newFakeCluster(t)
	bodies, tokens := patchRecorder(fc)
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(false)})

	rec := do(s, adminToken, http.MethodPost, "/api/storage/replication/hosts/host-1/evict", "")
	wantStatus(t, rec, http.StatusOK)
	if len(*bodies) != 1 || (*bodies)[0] != `{"spec":{"evict":["host-1"]}}` {
		t.Fatalf("patches = %q", *bodies)
	}
	if (*tokens)[0] != adminToken {
		t.Fatalf("patched as %q, want the caller", (*tokens)[0])
	}
}

func TestEvictHostReportsAPatchTheClusterRefuses(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodPatch, volumePath, func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	s := replicationServer(t, fc, &replicationSource{listed: twoCopies(false)})

	rec := do(s, adminToken, http.MethodPost, "/api/storage/replication/hosts/host-1/evict", "")
	wantStatus(t, rec, http.StatusOK)
	var out model.ReplicationEvict
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Moving) != 0 || len(out.Skipped) != 1 || out.Skipped[0].Volume != "pvc-1" {
		t.Fatalf("out = %+v", out)
	}
}
