// Chart assembly: naming resolved series and aligning them onto a shared time
// axis as chart-ready output.

package metrics

import (
	"sort"

	"github.com/epheo/dotvirt/internal/model"
)

// namedSeries is one resolved chart series: its display name and samples. The
// chart builders consume these whether they came from a fixed spec or were
// fanned out of a multi-series (byLabel / topk) query.
type namedSeries struct {
	name    string
	samples []sample
}

// buildChart aligns one chart's series onto a shared time axis (the union of
// all series' timestamps), with nil gaps where a series has no sample.
func buildChart(cs chartSpec, series []namedSeries) model.MetricChart {
	set := map[int64]struct{}{}
	for _, s := range series {
		for _, smp := range s.samples {
			set[smp.t] = struct{}{}
		}
	}
	times := make([]int64, 0, len(set))
	for t := range set {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	at := make(map[int64]int, len(times))
	for i, t := range times {
		at[t] = i
	}
	chart := model.MetricChart{Key: cs.key, Title: cs.title, Unit: cs.unit, Stacked: cs.stacked, Times: times, Series: make([]model.MetricSeries, len(series))}
	for si, s := range series {
		vals := make([]*float64, len(times))
		for _, smp := range s.samples {
			v := smp.v
			vals[at[smp.t]] = &v
		}
		chart.Series[si] = model.MetricSeries{Name: s.name, Values: vals}
	}
	return chart
}

// namedFromLabeled names topk result series by their namespace/name labels,
// sorted so colors stay stable across refreshes (topk order churns per step).
func namedFromLabeled(series []labeledSeries) []namedSeries {
	out := make([]namedSeries, 0, len(series))
	for _, ls := range series {
		out = append(out, namedSeries{name: seriesName(ls.labels), samples: ls.samples})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// seriesName labels one scope-chart series by the VM it tracks.
func seriesName(labels map[string]string) string {
	ns, name := labels["namespace"], labels["name"]
	switch {
	case ns != "" && name != "":
		return ns + "/" + name
	case name != "":
		return name
	default:
		return "value"
	}
}

// joinName composes a series display name from its spec prefix and the
// fanned-out label value ("Rx" + "eth0" → "Rx eth0").
func joinName(prefix, label string) string {
	switch {
	case label == "":
		return prefix
	case prefix == "":
		return label
	default:
		return prefix + " " + label
	}
}
