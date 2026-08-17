package model

// Performance and capacity: the Performance tab, summary widgets, host planes.

// HostCapacityNode is one worker's commitment picture: what is promised to
// VMs (vCPUs, guest memory) against what the node can offer.
type HostCapacityNode struct {
	Node           string  `json:"node"`
	CPUAllocatable float64 `json:"cpuAllocatable"`          // cores
	VCPUAllocated  float64 `json:"vcpuAllocated,omitempty"` // vCPUs committed to VMs
	MemAllocatable float64 `json:"memAllocatable"`          // bytes
	MemAllocated   float64 `json:"memAllocated,omitempty"`  // bytes committed to guests
}

// HostCapacity is the per-worker allocation/overcommit breakdown behind the
// host capacity card - the cluster overcommit ratios, per host.
type HostCapacity struct {
	Updated int64              `json:"updated"`
	Nodes   []HostCapacityNode `json:"nodes"`
}

// MetricSeries is one line in a chart: a value per timestamp in the parent
// MetricChart's Times grid (nil = a gap, no sample at that time).
type MetricSeries struct {
	Name   string     `json:"name"`
	Values []*float64 `json:"values"`
}

// MetricChart is one performance chart: a shared time axis plus its series, with a
// unit hint the UI formats by ("%", "bytes", "Bps", "iops", "ms").
type MetricChart struct {
	Key     string         `json:"key"`
	Title   string         `json:"title"`
	Unit    string         `json:"unit"`
	Stacked bool           `json:"stacked,omitempty"` // series partition a whole; render as stacked area
	Times   []int64        `json:"times"`             // unix seconds, the shared x-axis
	Series  []MetricSeries `json:"series"`
}

// VMMetrics is a VM's performance time-series for one range - several charts built
// from KubeVirt's kubevirt_vmi_* Prometheus metrics, shaped for direct charting.
type VMMetrics struct {
	Range   string        `json:"range"`
	StepSec int           `json:"stepSec"`
	Charts  []MetricChart `json:"charts"`
}

// UsageMetric is one resource's point-in-time usage for a VM Summary bar - Used of
// Total in the same unit, with a short recent history for an inline sparkline.
type UsageMetric struct {
	Used  float64   `json:"used"`
	Total float64   `json:"total,omitempty"` // 0 => no known denominator (show the value alone)
	Spark []float64 `json:"spark,omitempty"`
}

// VMUsage is a VM's live capacity-and-usage for the Summary tab: CPU % of
// allocated, memory used of allocated, guest-filesystem used of provisioned.
type VMUsage struct {
	Updated int64       `json:"updated"` // unix seconds ("Last updated")
	CPU     UsageMetric `json:"cpu"`     // Used = % of allocated vCPU, Total = 100
	Memory  UsageMetric `json:"memory"`  // bytes; Total = allocated (domain)
	Storage UsageMetric `json:"storage"` // bytes; guest filesystem used / capacity
}

// ClusterMetric is one aggregate resource for the cluster/infrastructure rings:
// Used now, Allocated (committed to VMs), of Total (node-allocatable capacity).
type ClusterMetric struct {
	Used      float64   `json:"used"`
	Allocated float64   `json:"allocated,omitempty"` // committed to VMs (vCPU / declared memory)
	Total     float64   `json:"total"`               // node-allocatable capacity (the boundary)
	Spark     []float64 `json:"spark,omitempty"`
}

// ConsumerVM is one row in a "top consumers" list (a VM ranked by a resource).
type ConsumerVM struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
}

// ClusterSummary is the aggregate capacity view for the "All VMs" landing:
// rings (used vs node-allocatable) + VM counts by
// phase + top-consumer VMs. VM-scoped sums are limited to the caller's namespaces;
// node capacity is the cluster-wide boundary.
type ClusterSummary struct {
	Updated   int64          `json:"updated"`
	CPU       ClusterMetric  `json:"cpu"`     // cores
	Memory    ClusterMetric  `json:"memory"`  // bytes
	Storage   ClusterMetric  `json:"storage"` // bytes
	VMs       map[string]int `json:"vms"`     // phase -> count
	TopCPU    []ConsumerVM   `json:"topCpu"`
	TopMemory []ConsumerVM   `json:"topMemory"`
}

// HostWorker is one worker in the utilization distribution. The full roster
// ships - bounded by node count, cached once for all callers - so the card
// can draw every worker as a point and name outliers client-side.
type HostWorker struct {
	Node          string  `json:"node"`
	Pct           float64 `json:"pct"`           // CPU utilization percent
	Mem           float64 `json:"mem,omitempty"` // memory utilization percent; 0 when the series is absent
	Unschedulable bool    `json:"unschedulable,omitempty"`
}

// HostBand is the DRS action band around the mean utilization - the deviation
// window KubeVirtRelieveAndMigrate actually triggers on. Workers above it are
// migration sources, workers below it are targets.
type HostBand struct {
	Low   float64 `json:"low"`   // percent
	High  float64 `json:"high"`  // percent
	Above int     `json:"above"` // workers over High
	Below int     `json:"below"` // workers under Low
}

// HostLoad is GET /api/metrics/hosts: the worker utilization distribution
// behind the DRS balance card - every worker with CPU and memory percent,
// hottest first. Band is set only when a DRS configuration is committed.
type HostLoad struct {
	Updated int64        `json:"updated"`
	Workers int          `json:"workers"`
	Mean    float64      `json:"mean"` // CPU percent
	Nodes   []HostWorker `json:"nodes"`
	Band    *HostBand    `json:"band,omitempty"`
}

// QuotaItem is one resource row of a ResourceQuota: current usage against the
// hard cap, pre-parsed for direct charting.
type QuotaItem struct {
	Resource string  `json:"resource"` // e.g. requests.cpu, requests.memory
	Used     float64 `json:"used"`
	Hard     float64 `json:"hard"`
	Unit     string  `json:"unit"` // cores | bytes | count
}

// NamespaceQuota is one ResourceQuota in one namespace - the project capacity
// band's input. A namespace may carry several (scoped) quotas.
type NamespaceQuota struct {
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	Items     []QuotaItem `json:"items"`
}
