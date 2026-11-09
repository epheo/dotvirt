package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/replication"
)

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
