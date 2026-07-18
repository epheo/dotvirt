// The curated chart catalog: UI range tiers and the PromQL specs behind each
// per-VM and per-scope chart.

package metrics

import (
	"fmt"
	"time"
)

// rangeSpec maps a UI range to a window + sample step, mirroring vCenter's tiers
// (real-time / day / week).
type rangeSpec struct {
	window time.Duration
	step   time.Duration
}

var ranges = map[string]rangeSpec{
	"1h":  {time.Hour, 30 * time.Second},
	"1d":  {24 * time.Hour, 5 * time.Minute},
	"1w":  {7 * 24 * time.Hour, 30 * time.Minute},
	"1mo": {30 * 24 * time.Hour, 2 * time.Hour}, // bounded by Prometheus retention in practice
}

const defaultRange = "1h"

// resolveRange maps a UI range to its spec, defaulting unknown ranges.
func resolveRange(rng string) (string, rangeSpec) {
	if spec, ok := ranges[rng]; ok {
		return rng, spec
	}
	return defaultRange, ranges[defaultRange]
}

// vcpuCount is the per-VM vCPU denominator. kubevirt_vmi_vcpu_count only
// exists on newer KubeVirt; older versions expose one series per vCPU instead,
// so fall back to counting those — dividing by the absent metric alone would
// blank every CPU-percentage chart.
func vcpuCount(sel string) string {
	return fmt.Sprintf("(kubevirt_vmi_vcpu_count%s or count by(namespace,name)(kubevirt_vmi_vcpu_seconds_total%s))", sel, sel)
}

// vcpuTotal is the scope-wide allocated-vCPU sum with the same fallback.
func vcpuTotal(sel string) string {
	return fmt.Sprintf("(sum(kubevirt_vmi_vcpu_count%s) or sum(count by(namespace,name)(kubevirt_vmi_vcpu_seconds_total%s)))", sel, sel)
}

type seriesSpec struct {
	name  string
	query string
	// byLabel marks a multi-series query: one chart series per result, named
	// "<name> <label value>" (per-NIC, per-drive). Empty = one fixed series.
	byLabel string
}

type chartSpec struct {
	key, title, unit string
	stacked          bool // render as a stacked area (parts of a whole)
	series           []seriesSpec
}

// chartSpecs builds the curated Overview charts for one VM — vCenter's CPU /
// Memory / Network / Disk, plus IOPS and disk latency. Network and disk break
// out per NIC / per drive. rw is the rate() window.
func chartSpecs(ns, name, rw string) []chartSpec {
	s := fmt.Sprintf("{namespace=%q,name=%q}", ns, name)
	return []chartSpec{
		{"cpu", "CPU", "%", false, []seriesSpec{
			{"Usage", fmt.Sprintf("rate(kubevirt_vmi_cpu_usage_seconds_total%s[%s])*100 / on(namespace,name) %s", s, rw, vcpuCount(s)), ""},
			{"Wait", fmt.Sprintf("rate(kubevirt_vmi_vcpu_wait_seconds_total%s[%s])*100", s, rw), ""},
			{"Steal", fmt.Sprintf("rate(kubevirt_vmi_vcpu_delay_seconds_total%s[%s])*100", s, rw), ""},
		}},
		// Used/Cached/Free partition the guest's memory — a stacked area whose
		// top edge is the domain total, like vCenter's stacked memory chart.
		{"memory", "Memory", "bytes", true, []seriesSpec{
			{"Used", fmt.Sprintf("kubevirt_vmi_memory_used_bytes%s", s), ""},
			{"Cached", fmt.Sprintf("kubevirt_vmi_memory_cached_bytes%s", s), ""},
			{"Free", fmt.Sprintf("kubevirt_vmi_memory_unused_bytes%s", s), ""},
		}},
		{"network", "Network", "Bps", false, []seriesSpec{
			{"Rx", fmt.Sprintf("sum by(interface)(rate(kubevirt_vmi_network_receive_bytes_total%s[%s]))", s, rw), "interface"},
			{"Tx", fmt.Sprintf("sum by(interface)(rate(kubevirt_vmi_network_transmit_bytes_total%s[%s]))", s, rw), "interface"},
		}},
		{"disk", "Disk throughput", "Bps", false, []seriesSpec{
			{"Read", fmt.Sprintf("sum by(drive)(rate(kubevirt_vmi_storage_read_traffic_bytes_total%s[%s]))", s, rw), "drive"},
			{"Write", fmt.Sprintf("sum by(drive)(rate(kubevirt_vmi_storage_write_traffic_bytes_total%s[%s]))", s, rw), "drive"},
		}},
		{"iops", "Disk IOPS", "iops", false, []seriesSpec{
			{"Read", fmt.Sprintf("sum by(drive)(rate(kubevirt_vmi_storage_iops_read_total%s[%s]))", s, rw), "drive"},
			{"Write", fmt.Sprintf("sum by(drive)(rate(kubevirt_vmi_storage_iops_write_total%s[%s]))", s, rw), "drive"},
		}},
		{"latency", "Disk latency", "ms", false, []seriesSpec{
			{"Read", fmt.Sprintf("sum(rate(kubevirt_vmi_storage_read_times_seconds_total%s[%s])) / sum(rate(kubevirt_vmi_storage_iops_read_total%s[%s])) * 1000", s, rw, s, rw), ""},
			{"Write", fmt.Sprintf("sum(rate(kubevirt_vmi_storage_write_times_seconds_total%s[%s])) / sum(rate(kubevirt_vmi_storage_iops_write_total%s[%s])) * 1000", s, rw, s, rw), ""},
		}},
	}
}

// scopeChartSpecs builds the per-VM top-consumer charts for a container scope
// (the whole inventory, a project, a namespace, or a node). Each chart is ONE
// topk query whose result series are the heaviest VMs, labeled namespace/name.
// sel is the namespace(+node) selector, rw the rate() window.
func scopeChartSpecs(sel, rw string) []chartSpec {
	topk := func(expr string) string { return fmt.Sprintf("topk(5, sum by(namespace,name)(%s))", expr) }
	rate := func(metric string) string { return fmt.Sprintf("rate(%s%s[%s])", metric, sel, rw) }
	return []chartSpec{
		{"cpu", "CPU — top VMs", "cores", false, []seriesSpec{
			{"", topk(rate("kubevirt_vmi_cpu_usage_seconds_total")), ""},
		}},
		{"memory", "Memory — top VMs", "bytes", false, []seriesSpec{
			{"", topk(fmt.Sprintf("kubevirt_vmi_memory_used_bytes%s", sel)), ""},
		}},
		{"network", "Network — top VMs", "Bps", false, []seriesSpec{
			{"", topk(rate("kubevirt_vmi_network_receive_bytes_total") + " + " + rate("kubevirt_vmi_network_transmit_bytes_total")), ""},
		}},
		{"disk", "Disk throughput — top VMs", "Bps", false, []seriesSpec{
			{"", topk(rate("kubevirt_vmi_storage_read_traffic_bytes_total") + " + " + rate("kubevirt_vmi_storage_write_traffic_bytes_total")), ""},
		}},
	}
}
