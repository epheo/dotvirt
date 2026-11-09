package modules

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

type fakeSource struct {
	served  map[string]bool
	classes bool
	err     map[string]error
}

func (f *fakeSource) ServesAPIResource(gvr schema.GroupVersionResource) (bool, error) {
	if err := f.err[gvr.Resource]; err != nil {
		return false, err
	}
	return f.served[gvr.Resource], nil
}

func (f *fakeSource) HasAny(_ context.Context, gvr schema.GroupVersionResource) (bool, error) {
	if err := f.err[gvr.Resource]; err != nil {
		return false, err
	}
	return f.classes, nil
}

func TestEachModuleFollowsItsAPI(t *testing.T) {
	src := &fakeSource{served: map[string]bool{"virtualmachines": true, "volumes": true}, classes: true}
	s := New(src)
	s.probe(context.Background())
	got, ok := s.Get()
	want := model.Modules{VMs: true, ReplicatedStorage: true, Routes: true}
	if !ok || got != want {
		t.Errorf("Get = %+v, %v; want %+v", got, ok, want)
	}
}

// MicroShift serves the Gateway API with no controller until portail runs.
func TestRoutesNeedAGatewayClassNotTheCRDs(t *testing.T) {
	src := &fakeSource{served: map[string]bool{gatewayClasses.Resource: true}}
	s := New(src)
	s.probe(context.Background())
	if got, _ := s.Get(); got.Routes {
		t.Error("routes on without a GatewayClass")
	}
}

// All false would hide the whole console on a discovery blip at boot.
func TestAPartialFirstAnswerIsNoAnswer(t *testing.T) {
	src := &fakeSource{served: map[string]bool{"virtualmachines": true}, err: map[string]error{"volumes": errors.New("down")}}
	s := New(src)
	s.probe(context.Background())
	if _, ok := s.Get(); ok {
		t.Error("a partial first answer was served")
	}
	src.err = nil
	s.probe(context.Background())
	if got, ok := s.Get(); !ok || !got.VMs {
		t.Errorf("after recovery: %+v, %v", got, ok)
	}
}

func TestAFailedReadKeepsTheLastAnswer(t *testing.T) {
	vms := cluster.GVR(model.KindVM).Resource
	src := &fakeSource{served: map[string]bool{vms: true}}
	s := New(src)
	s.probe(context.Background())
	src.err = map[string]error{vms: errors.New("down")}
	s.probe(context.Background())
	if got, ok := s.Get(); !ok || !got.VMs {
		t.Errorf("a failed read turned VMs off: %+v, %v", got, ok)
	}
}

// A CRD group can be missing from discovery for a moment at apiserver start.
func TestAModuleGoesOffOnlyOnASecondNoInARow(t *testing.T) {
	src := &fakeSource{served: map[string]bool{"virtualmachines": true}}
	s := New(src)
	on := func(want bool, when string) {
		t.Helper()
		s.probe(context.Background())
		if got, _ := s.Get(); got.VMs != want {
			t.Errorf("%s: VMs = %v, want %v", when, got.VMs, want)
		}
	}
	on(true, "served")
	src.served["virtualmachines"] = false
	on(true, "first no")
	src.served["virtualmachines"] = true
	on(true, "back")
	src.served["virtualmachines"] = false
	on(true, "a new first no")
	on(false, "second no in a row")
}
