package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/modules"
)

type servesVMsOnly struct{}

func (servesVMsOnly) ServesAPIResource(gvr schema.GroupVersionResource) (bool, error) {
	return gvr.Resource == "virtualmachines", nil
}

func (servesVMsOnly) HasAny(context.Context, schema.GroupVersionResource) (bool, error) {
	return false, nil
}

// Until discovery answers, the web hides nothing: it reads a failure, not all false.
func TestModulesFailUntilKnown(t *testing.T) {
	s := newFakeCluster(t).server(nil, Config{})
	s.modules = modules.New(servesVMsOnly{})
	wantStatus(t, do(s, adminToken, http.MethodGet, "/api/modules", ""), http.StatusServiceUnavailable)
}

func TestModulesAnswerWhatTheClusterServes(t *testing.T) {
	s := newFakeCluster(t).server(nil, Config{})
	snap := modules.New(servesVMsOnly{})
	snap.Run(t.Context())
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, ok := snap.Get(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no answer")
		}
		time.Sleep(time.Millisecond)
	}
	s.modules = snap
	rec := do(s, adminToken, http.MethodGet, "/api/modules", "")
	wantStatus(t, rec, http.StatusOK)
	var got model.Modules
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (model.Modules{VMs: true}) {
		t.Errorf("modules = %+v", got)
	}
}
