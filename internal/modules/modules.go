// Package modules answers which parts of the console the cluster serves, so
// no module asks for itself and no page guesses.
package modules

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/drs"
	"github.com/epheo/dotvirt/internal/model"
)

// interval is reflect's discovery pace: a module installed later appears
// alongside the watches behind it.
const interval = time.Minute

// Source is the cluster as the probe reads it; *cluster.Client in production.
type Source interface {
	ServesAPIResource(schema.GroupVersionResource) (bool, error)
	HasAny(context.Context, schema.GroupVersionResource) (bool, error)
}

// served are the modules a served API turns on.
var served = []struct {
	gvr schema.GroupVersionResource
	set func(*model.Modules, bool)
}{
	{cluster.GVR(model.KindVM), func(m *model.Modules, on bool) { m.VMs = on }},
	{schema.GroupVersionResource{Group: "vdi.desku.be", Version: "v1alpha1", Resource: "vdiinstances"}, func(m *model.Modules, on bool) { m.Desktops = on }},
	{cluster.GVR(model.KindUDN), func(m *model.Modules, on bool) { m.Networks = on }},
	{drs.GVR, func(m *model.Modules, on bool) { m.Rebalancing = on }},
	{schema.GroupVersionResource{Group: "r-drbd.io", Version: "v1alpha1", Resource: "volumes"}, func(m *model.Modules, on bool) { m.ReplicatedStorage = on }},
}

// gatewayClasses turn on routes. Not the CRDs: our MicroShift ships those with
// no controller behind them until portail runs.
var gatewayClasses = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}

// Snapshot is the probe's latest answer: the API layer reads it per request,
// so no request touches discovery.
type Snapshot struct {
	src   Source
	value atomic.Pointer[model.Modules]
}

func New(src Source) *Snapshot { return &Snapshot{src: src} }

// Run probes now and on the discovery pace until ctx is cancelled. It returns
// at once: an unreachable apiserver must not hold up startup.
func (s *Snapshot) Run(ctx context.Context) {
	go func() {
		s.probe(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.probe(ctx)
			}
		}
	}()
}

// Get returns the answer; false until every API has answered at once. All
// false would hide the whole console, so a partial first answer is no answer.
func (s *Snapshot) Get() (model.Modules, bool) {
	v := s.value.Load()
	if v == nil {
		return model.Modules{}, false
	}
	return *v, true
}

// probe keeps a module's last answer when its read fails.
func (s *Snapshot) probe(ctx context.Context) {
	var next model.Modules
	prev := s.value.Load()
	if prev != nil {
		next = *prev
	}
	complete := true
	read := func(what schema.GroupVersionResource, set func(*model.Modules, bool), ask func() (bool, error)) {
		on, err := ask()
		if err != nil {
			log.Printf("modules: read %s: %v", what.GroupResource(), err)
			complete = false
			return
		}
		set(&next, on)
	}
	for _, m := range served {
		read(m.gvr, m.set, func() (bool, error) { return s.src.ServesAPIResource(m.gvr) })
	}
	read(gatewayClasses, func(m *model.Modules, on bool) { m.Routes = on }, func() (bool, error) { return s.src.HasAny(ctx, gatewayClasses) })
	if prev != nil || complete {
		s.value.Store(&next)
	}
}
