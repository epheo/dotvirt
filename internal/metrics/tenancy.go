// The multi-namespace read path: one cluster-wide query for a caller who may
// make one, else one tenancy query per namespace, folded into the answer the
// single query would have given.

package metrics

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/epheo/dotvirt/internal/restfactory"
)

// Caller is who a read runs as and how far one of its queries may reach.
type Caller struct {
	Token string
	// Wide: the caller holds the right the querier's cluster-wide port
	// checks, so one query spans every namespace. Otherwise each namespace is
	// its own query on the tenancy port, which checks the caller's metrics
	// right in that namespace and pins the query to it.
	Wide bool
}

// in is the namespace one of who's queries about ns is pinned to: none for a
// wide caller, whose queries go to the cluster-wide port.
func (who Caller) in(ns string) string {
	if who.Wide {
		return ""
	}
	return ns
}

// key is who's cache key for a view: one per view for every wide caller, one
// per view and token for anyone else.
func (who Caller) key(view string) string {
	if who.Wide {
		return "wide|" + view
	}
	return "user|" + restfactory.TokenKey(who.Token) + "|" + view
}

// over is the namespaces one view query spans, and whether one query may span
// them all.
type over struct {
	wide bool
	nss  []string
}

// nsMatch matches exactly nss. For one namespace it is the very matcher the
// tenancy port's label proxy injects, which that proxy refuses to see
// replaced by a different one.
func nsMatch(nss []string) string {
	switch len(nss) {
	case 0:
		// =~"" would match every series without the label.
		return `namespace="__dotvirt_none__"`
	case 1:
		return fmt.Sprintf("namespace=%q", nss[0])
	}
	return fmt.Sprintf("namespace=~%q", strings.Join(nss, "|"))
}

// each calls fn for every namespace, at most viewConcurrency at once.
func each(nss []string, fn func(i int, ns string)) {
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(len(nss), viewConcurrency) {
		wg.Go(func() {
			for i := range next {
				fn(i, nss[i])
			}
		})
	}
	for i := range nss {
		next <- i
	}
	close(next)
	wg.Wait()
}

// vectorOver runs one instant query over o: q(o.nss) once when o is wide,
// else q([ns]) per namespace on the tenancy port, the answers folded. q must
// aggregate by sum, or keep the namespace label, for the fold to equal the
// single query's answer. No namespace queries nothing.
func (r *read) vectorOver(ctx context.Context, o over, q func(nss []string) string) []labeledValue {
	switch {
	case len(o.nss) == 0:
		return nil
	case o.wide:
		return r.vector(ctx, "", q(o.nss))
	}
	parts := make([][]labeledValue, len(o.nss))
	each(o.nss, func(i int, ns string) { parts[i] = r.vector(ctx, ns, q([]string{ns})) })
	return foldVectors(parts)
}

// seriesOver is vectorOver for a range query.
func (r *read) seriesOver(ctx context.Context, o over, w span, q func(nss []string) string) []labeledSeries {
	switch {
	case len(o.nss) == 0:
		return nil
	case o.wide:
		return r.series(ctx, "", q(o.nss), w)
	}
	parts := make([][]labeledSeries, len(o.nss))
	each(o.nss, func(i int, ns string) { parts[i] = r.series(ctx, ns, q([]string{ns}), w) })
	return foldSeries(parts)
}

// labelKey is a label set's identity.
func labelKey(labels map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(labels[k])
		b.WriteByte(0)
	}
	return b.String()
}

// foldVectors adds per-namespace instant answers into one: series with the
// same labels sum, as an outer sum over every namespace would.
func foldVectors(parts [][]labeledValue) []labeledValue {
	at := map[string]int{}
	var out []labeledValue
	for _, part := range parts {
		for _, lv := range part {
			k := labelKey(lv.labels)
			if i, ok := at[k]; ok {
				out[i].value += lv.value
				continue
			}
			at[k] = len(out)
			out = append(out, lv)
		}
	}
	return out
}

// foldSeries is foldVectors per timestamp.
func foldSeries(parts [][]labeledSeries) []labeledSeries {
	at := map[string]int{}
	var out []labeledSeries
	for _, part := range parts {
		for _, ls := range part {
			k := labelKey(ls.labels)
			if i, ok := at[k]; ok {
				out[i].samples = addSamples(out[i].samples, ls.samples)
				continue
			}
			at[k] = len(out)
			out = append(out, ls)
		}
	}
	return out
}

// addSamples merges two time-ordered sample lists, summing shared timestamps.
func addSamples(a, b []sample) []sample {
	out := make([]sample, 0, max(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i].t < b[j].t:
			out = append(out, a[i])
			i++
		case b[j].t < a[i].t:
			out = append(out, b[j])
			j++
		default:
			out = append(out, sample{t: a[i].t, v: a[i].v + b[j].v})
			i, j = i+1, j+1
		}
	}
	out = append(out, a[i:]...)
	return append(out, b[j:]...)
}

// topKSeries keeps the k largest values at each timestamp, as topk does per
// step, and drops the series left with no sample: per-namespace topk answers
// folded this way are the cluster-wide topk. A single topk answer passes
// through unchanged.
func topKSeries(series []labeledSeries, k int) []labeledSeries {
	type point struct {
		s int
		v float64
	}
	byT := map[int64][]point{}
	for si, ls := range series {
		for _, smp := range ls.samples {
			byT[smp.t] = append(byT[smp.t], point{si, smp.v})
		}
	}
	keep := map[[2]int64]bool{}
	for t, pts := range byT {
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].v > pts[j].v })
		for _, p := range pts[:min(k, len(pts))] {
			keep[[2]int64{int64(p.s), t}] = true
		}
	}
	var out []labeledSeries
	for si, ls := range series {
		var kept []sample
		for _, smp := range ls.samples {
			if keep[[2]int64{int64(si), smp.t}] {
				kept = append(kept, smp)
			}
		}
		if len(kept) > 0 {
			out = append(out, labeledSeries{labels: ls.labels, samples: kept})
		}
	}
	return out
}
