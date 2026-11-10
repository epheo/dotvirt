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
	// Discovery did not answer for OLM's kinds: only a known no hides.
	if got != (model.Modules{VMs: true, OLM: true, MachineConfig: true}) {
		t.Errorf("modules = %+v", got)
	}
}

// Rebalancing installs through OLM and its PSI option adds a MachineConfig:
// the modules say which this cluster serves, so the web can say why neither
// is offered rather than read a refused capability as a missing right.
func TestModulesSayWhatTheClusterCanInstall(t *testing.T) {
	for _, c := range []struct {
		name          string
		serves        []model.Kind
		olm, machCfgs bool
	}{
		{"no OLM", []model.Kind{model.KindNS}, false, false},
		{"OLM, no machine config", []model.Kind{model.KindNS, model.KindSub, model.KindOpGroup}, true, false},
		{"both", []model.Kind{model.KindNS, model.KindSub, model.KindOpGroup, model.KindMachCfg}, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := newFakeCluster(t)
			fc.serveKinds(c.serves...)
			s := fc.server(nil, Config{})
			s.modules = knownModules(t, true)
			got := decodeBody[model.Modules](t, do(s, adminToken, http.MethodGet, "/api/modules", ""))
			if got.OLM != c.olm || got.MachineConfig != c.machCfgs {
				t.Errorf("modules = %+v, want olm %v, machineConfig %v", got, c.olm, c.machCfgs)
			}
		})
	}
}
