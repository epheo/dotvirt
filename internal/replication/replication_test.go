package replication

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
)

const secret = "c2VjcmV0LXNoYXJlZC1ieS1kcmJkLXBlZXJz"

func obj(name string, content map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: content}
	u.SetName(name)
	return u
}

func report(node, disk string, peers map[string]any, extra map[string]any) any {
	r := map[string]any{"node": node, "present": true, "diskState": disk}
	if peers != nil {
		r["peers"] = peers
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func assigned(pairs ...string) []any {
	var out []any
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, map[string]any{"node": pairs[i], "role": pairs[i+1], "pool": "p-" + pairs[i], "nodeId": int64(i), "port": int64(11000)})
	}
	return out
}

func volume(name string, copies int64, status map[string]any, replicas ...string) *unstructured.Unstructured {
	return obj(name, map[string]any{
		"spec": map[string]any{
			"sizeBytes": int64(1 << 30), "replicas": copies, "tier": "nvme",
			"assignment": map[string]any{"replicas": assigned(replicas...), "minor": int64(7), "secret": secret},
		},
		"status": status,
	})
}

func ready(reports ...any) map[string]any {
	return map[string]any{"phase": "Ready", "primary": "host-1", "replicas": reports}
}

func pv(name, driver, phase, ns, claimName string) *unstructured.Unstructured {
	return obj(name, map[string]any{
		"spec": map[string]any{
			"csi":      map[string]any{"driver": driver},
			"claimRef": map[string]any{"namespace": ns, "name": claimName},
		},
		"status": map[string]any{"phase": phase},
	})
}

func connected(nodes ...string) map[string]any {
	out := map[string]any{}
	for _, n := range nodes {
		out[n] = "Connected"
	}
	return out
}

func healthyTwoCopies(name string) *unstructured.Unstructured {
	return volume(name, 2, ready(
		report("host-1", "UpToDate", connected("host-2", "host-3"), map[string]any{"inUse": true}),
		report("host-2", "UpToDate", connected("host-1", "host-3"), nil),
		report("host-3", "Diskless", connected("host-1", "host-2"), nil),
	), "host-1", "diskful", "host-2", "diskful", "host-3", "tiebreaker")
}

func newSnapshot(t *testing.T) (*Snapshot, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New()
	return New(nil, bus, func() bool { return true }), bus
}

func mustAdd(t *testing.T, s interface{ Add(any) error }, o any) {
	t.Helper()
	if err := s.Add(o); err != nil {
		t.Fatal(err)
	}
}

func TestSecretIsNeverHeld(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-a"))
	mustAdd(t, s.claims, pv("pvc-a", driver, "Bound", "team-a", "root"))
	if got := fmt.Sprintf("%+v", s.volumes.rows); strings.Contains(got, secret) {
		t.Fatalf("the projection holds the secret: %s", got)
	}
	body, err := json.Marshal(s.Overview())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("the overview carries the secret: %s", body)
	}
}

func TestReplicaStates(t *testing.T) {
	percent := int64(64)
	cases := []struct {
		name string
		vol  *unstructured.Unstructured
		want map[string]string
	}{
		{"in sync, with a witness", healthyTwoCopies("v"), map[string]string{
			"host-1": model.ReplicaInSync, "host-2": model.ReplicaInSync, "host-3": model.ReplicaStandby,
		}},
		{"catching up", volume("v", 2, ready(
			report("host-1", "UpToDate", connected("host-2"), nil),
			report("host-2", "Inconsistent", connected("host-1"), map[string]any{"syncPercent": percent}),
		), "host-1", "diskful", "host-2", "diskful"), map[string]string{
			"host-1": model.ReplicaInSync, "host-2": model.ReplicaCatchingUp,
		}},
		{"a dead host's last report does not count", volume("v", 2, ready(
			report("host-1", "UpToDate", connected("host-2", "host-3"), nil),
			report("host-2", "UpToDate", map[string]any{"host-1": "Connecting", "host-3": "Connected"}, nil),
			report("host-3", "Diskless", map[string]any{"host-1": "Connecting", "host-2": "Connected"}, nil),
		), "host-1", "diskful", "host-2", "diskful", "host-3", "tiebreaker"), map[string]string{
			"host-1": model.ReplicaUnreachable, "host-2": model.ReplicaInSync, "host-3": model.ReplicaStandby,
		}},
		{"an isolated host does not mark its peers", volume("v", 3, ready(
			report("host-1", "UpToDate", map[string]any{"host-2": "Connecting", "host-3": "Connecting"}, nil),
			report("host-2", "UpToDate", map[string]any{"host-1": "Connecting", "host-3": "Connected"}, nil),
			report("host-3", "UpToDate", map[string]any{"host-1": "Connecting", "host-2": "Connected"}, nil),
		), "host-1", "diskful", "host-2", "diskful", "host-3", "diskful"), map[string]string{
			"host-1": model.ReplicaUnreachable, "host-2": model.ReplicaInSync, "host-3": model.ReplicaInSync,
		}},
		{"assigned, no report yet", volume("v", 1, map[string]any{"phase": "Provisioning"}, "host-1", "diskful"), map[string]string{
			"host-1": model.ReplicaUnknown,
		}},
		{"outdated", volume("v", 2, ready(
			report("host-1", "UpToDate", connected("host-2"), nil),
			report("host-2", "Outdated", connected("host-1"), nil),
		), "host-1", "diskful", "host-2", "diskful"), map[string]string{
			"host-1": model.ReplicaInSync, "host-2": model.ReplicaBehind,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, _ := decodeVolume(c.vol)
			for _, r := range v.Replicas {
				if r.State != c.want[r.Node] {
					t.Errorf("%s: state %q, want %q", r.Node, r.State, c.want[r.Node])
				}
			}
		})
	}
}

func TestStanding(t *testing.T) {
	inSync := model.VolumeReplica{Role: "diskful", State: model.ReplicaInSync}
	lost := model.VolumeReplica{Role: "diskful", State: model.ReplicaUnreachable}
	p := 40
	catching := model.VolumeReplica{Role: "diskful", State: model.ReplicaCatchingUp, SyncPercent: &p}
	cases := []struct {
		name string
		v    model.ReplicatedVolume
		want string
	}{
		{"every copy in sync", model.ReplicatedVolume{Phase: "Ready", Copies: 2, Replicas: []model.VolumeReplica{inSync, inSync}}, model.StandingProtected},
		{"one copy by choice", model.ReplicatedVolume{Phase: "Ready", Copies: 1, Replicas: []model.VolumeReplica{inSync}}, model.StandingLocal},
		{"catching up", model.ReplicatedVolume{Phase: "Degraded", Copies: 2, Replicas: []model.VolumeReplica{inSync, catching}}, model.StandingCatchingUp},
		{"a copy lost", model.ReplicatedVolume{Phase: "Degraded", Copies: 2, Replicas: []model.VolumeReplica{inSync, lost}}, model.StandingExposed},
		{"no copy serving", model.ReplicatedVolume{Phase: "Degraded", Copies: 2, Replicas: []model.VolumeReplica{lost, lost}}, model.StandingNeedsOperator},
		{"split brain", model.ReplicatedVolume{Phase: "Degraded", SplitBrain: true, Copies: 2, Replicas: []model.VolumeReplica{inSync, inSync}}, model.StandingNeedsOperator},
		{"quorum lost", model.ReplicatedVolume{Phase: "Degraded", QuorumLost: true, Copies: 2, Replicas: []model.VolumeReplica{inSync}}, model.StandingNeedsOperator},
		{"failed", model.ReplicatedVolume{Phase: "Failed", Copies: 1}, model.StandingNeedsOperator},
		{"provisioning", model.ReplicatedVolume{Phase: "Provisioning", Copies: 2}, model.StandingPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, got := standing(c.v); got != c.want {
				t.Errorf("standing %q, want %q", got, c.want)
			}
		})
	}
}

func TestProtectionShowsOnlyTheCallersDisksAndNoHost(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-a"))
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-b"))
	mustAdd(t, s.claims, pv("pvc-a", driver, "Bound", "team-a", "root"))
	mustAdd(t, s.claims, pv("pvc-b", driver, "Bound", "team-b", "other"))

	got := s.Protection([]string{"team-a"})
	if len(got) != 1 || got[0].Namespace != "team-a" || got[0].Claim != "root" {
		t.Fatalf("protection = %+v", got)
	}
	body, _ := json.Marshal(got)
	for _, leak := range []string{"host-", "p-host", "team-b", "other"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("a tenant row carries %q: %s", leak, body)
		}
	}
}

// A Released PersistentVolume still names its old claim, and that namespace
// may belong to someone else by now.
func TestProtectionIgnoresAReleasedVolume(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-a"))
	mustAdd(t, s.claims, pv("pvc-a", driver, "Released", "team-a", "root"))
	if got := s.Protection([]string{"team-a"}); len(got) != 0 {
		t.Fatalf("protection = %+v", got)
	}
}

func TestAnotherDriversVolumeIsNotJoined(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-a"))
	mustAdd(t, s.claims, pv("pvc-a", "other.csi", "Bound", "team-a", "root"))
	if got := s.Protection([]string{"team-a"}); len(got) != 0 {
		t.Fatalf("protection = %+v", got)
	}
}

func TestOnlyAMoveTheConsoleShowsPublishes(t *testing.T) {
	s, bus := newSnapshot(t)
	if err := s.volumes.Add(healthyTwoCopies("pvc-a")); err != nil {
		t.Fatal(err)
	}
	before := bus.Version(eventbus.ReplicationChanged)
	again := healthyTwoCopies("pvc-a")
	again.SetResourceVersion("99")
	if err := s.volumes.Update(again); err != nil {
		t.Fatal(err)
	}
	if bus.Version(eventbus.ReplicationChanged) != before {
		t.Fatal("a write that moved nothing published")
	}
	moved := volume("pvc-a", 2, ready(report("host-1", "UpToDate", nil, nil)), "host-1", "diskful")
	if err := s.volumes.Update(moved); err != nil {
		t.Fatal(err)
	}
	if bus.Version(eventbus.ReplicationChanged) == before {
		t.Fatal("a real move did not publish")
	}
}

func TestATenantsLevelMovesWithItsOwnDisksOnly(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.claims, pv("pvc-a", driver, "Bound", "team-a", "root"))
	mustAdd(t, s.claims, pv("pvc-b", driver, "Bound", "team-b", "other"))
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-a"))
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-b"))
	a, total := s.Gen("team-a"), s.Total()

	if err := s.volumes.Update(volume("pvc-b", 2, ready(report("host-1", "Outdated", nil, nil)), "host-1", "diskful")); err != nil {
		t.Fatal(err)
	}
	if s.Gen("team-a") != a {
		t.Fatal("another tenant's disk moved this tenant's level")
	}
	if s.Total() == total {
		t.Fatal("an operator's level did not move")
	}
}

func TestReplaceKeepsTheLevelWhenNothingMoved(t *testing.T) {
	s, bus := newSnapshot(t)
	list := []any{healthyTwoCopies("pvc-a")}
	if err := s.volumes.Replace(list, "1"); err != nil {
		t.Fatal(err)
	}
	before := bus.Version(eventbus.ReplicationChanged)
	if err := s.volumes.Replace([]any{healthyTwoCopies("pvc-a")}, "2"); err != nil {
		t.Fatal(err)
	}
	if bus.Version(eventbus.ReplicationChanged) != before {
		t.Fatal("a relist that changed nothing published")
	}
	if err := s.volumes.Replace(nil, "3"); err != nil {
		t.Fatal(err)
	}
	if len(s.Overview().Volumes) != 0 {
		t.Fatal("a volume gone from the relist stayed")
	}
}

func TestOverviewJoinsTheClaimAndPools(t *testing.T) {
	s, _ := newSnapshot(t)
	mustAdd(t, s.volumes, healthyTwoCopies("pvc-a"))
	mustAdd(t, s.claims, pv("pvc-a", driver, "Bound", "team-a", "root"))
	mustAdd(t, s.pools, obj("nvme-host-1", map[string]any{
		"spec": map[string]any{"node": "host-1", "tier": "nvme"},
		"status": map[string]any{
			"totalBytes": int64(100), "freeBytes": int64(60), "placeableBytes": int64(50),
			"conditions": []any{map[string]any{"type": "Available", "status": "True", "reason": "ThinPoolReady"}},
		},
	}))
	mustAdd(t, s.hosts, obj("host-5", map[string]any{"status": map[string]any{"disks": []any{
		map[string]any{"path": "/dev/disk/by-id/wwn-1", "state": "Blank", "sizeBytes": int64(9), "model": "X"},
		map[string]any{"path": "/dev/disk/by-id/wwn-2", "state": "Member", "sizeBytes": int64(9)},
	}}}))

	got := s.Overview()
	if len(got.Volumes) != 1 || got.Volumes[0].Namespace != "team-a" || got.Volumes[0].Claim != "root" {
		t.Fatalf("volumes = %+v", got.Volumes)
	}
	if len(got.Pools) != 1 || !got.Pools[0].Available || got.Pools[0].PlaceableBytes != 50 {
		t.Fatalf("pools = %+v", got.Pools)
	}
	if len(got.Hosts) != 1 || len(got.Hosts[0].Blank) != 1 {
		t.Fatalf("hosts = %+v", got.Hosts)
	}
}

func TestSyncedWaitsForEveryKind(t *testing.T) {
	s, _ := newSnapshot(t)
	for i, table := range []interface {
		Replace([]any, string) error
	}{s.volumes, s.pools, s.hosts, s.claims} {
		if s.Overview().Synced {
			t.Fatalf("synced after %d of 4 lists", i)
		}
		if err := table.Replace(nil, "1"); err != nil {
			t.Fatal(err)
		}
	}
	if !s.Overview().Synced {
		t.Fatal("not synced after every list")
	}
}
