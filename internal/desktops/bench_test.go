package desktops

import (
	"fmt"
	"testing"
)

// BenchmarkHeldIn is one caller's per-frame review set at 5000 desktops,
// pools and claims in 500 namespaces, for a caller in 20 of them.
func BenchmarkHeldIn(b *testing.B) {
	s := New(nil, nil, func() bool { return true })
	s.started.Store(true)
	for i := range 5000 {
		ns := fmt.Sprintf("ns-%d", i%500)
		_ = s.pools.Add(pool(ns, fmt.Sprintf("pool-%d", i), nil))
		_ = s.instances.Add(desktop(ns, fmt.Sprintf("desk-%d", i), nil))
		_ = s.claims.Add(claim(ns, fmt.Sprintf("claim-%d", i), map[string]any{"poolName": "lab", "user": "u"}, nil))
	}
	var mine []string
	for i := range 20 {
		mine = append(mine, fmt.Sprintf("ns-%d", i))
	}
	mine = append(mine, "")
	b.ReportAllocs()
	for b.Loop() {
		_ = s.HeldIn(mine)
	}
}
