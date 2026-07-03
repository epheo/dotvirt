// Package drsgen renders the Kube Descheduler Operator manifests behind
// dotvirt's "DRS" panel — cluster-wide automatic VM rebalancing, the vSphere
// DRS analog — from a small spec, the way netgen renders networks. Owns-nothing:
// the output is proposed via PR into the platform repo and applied by Argo,
// never written to the cluster. All output paths are constants (no user input
// ever becomes a path segment) and every field is enum/range-validated.
package drsgen

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// Namespace is the operator's install namespace — fixed by the Kube Descheduler
// Operator (its CSV only supports own-namespace install there).
const Namespace = "openshift-kube-descheduler-operator"

// Platform-repo paths. CRPath is the KubeDescheduler CR: its presence on the
// base branch is what "DRS is configured" means, and it is the one file a
// disable removes (the operator install stays). PSIPath is the worker PSI
// kernel-arg MachineConfig the load-aware profile needs — staged only on
// explicit request because applying it reboots the worker pool.
const (
	NamespacePath     = "descheduler/namespace.yaml"
	OperatorGroupPath = "descheduler/operatorgroup.yaml"
	SubscriptionPath  = "descheduler/subscription.yaml"
	CRPath            = "descheduler/kubedescheduler.yaml"
	PSIPath           = "machineconfigs/99-worker-psi.yaml"
)

// Automation modes — vSphere DRS "Manual" vs "Fully Automated".
const (
	ModePredictive = "Predictive" // dry-run: logs/metrics what would migrate, moves nothing
	ModeAutomatic  = "Automatic"  // evicts, so VMs live-migrate to rebalance
)

// Deviation thresholds — the DRS migration-aggressiveness slider. AsymmetricLow
// (0%:10%) only flags clearly-hot nodes and never over-drains an idle one;
// Low/Medium/High (10/20/30% both ways) are progressively more eager to move VMs.
var validThresholds = map[string]bool{
	"AsymmetricLow": true,
	"Low":           true,
	"Medium":        true,
	"High":          true,
}

// Defaults applied when a field is zero: conservative rebalancing on a
// production-paced interval, with migration concurrency matching KubeVirt's
// stock live-migration limits.
const (
	defaultThreshold  = "AsymmetricLow"
	defaultInterval   = 60
	defaultNodeLimit  = 2 // parallelOutboundMigrationsPerNode
	defaultTotalLimit = 5 // parallelMigrationsPerCluster
)

// Spec describes the DRS configuration to render. Zero values take the
// defaults above; Mode is required (enabling automatic live-migration must be
// an explicit choice, never a fallthrough).
type Spec struct {
	Mode      string `json:"mode"`                // Predictive | Automatic
	Threshold string `json:"threshold,omitempty"` // AsymmetricLow | Low | Medium | High

	// IntervalSeconds is how often the descheduler re-evaluates the cluster
	// (the DRS invocation period).
	IntervalSeconds int `json:"intervalSeconds,omitempty"`

	// SoftTainter applies PreferNoSchedule taints to hot nodes so the scheduler
	// also stops placing new VMs there, closing the loop with initial placement.
	// Nil means enabled.
	SoftTainter *bool `json:"softTainter,omitempty"`

	// Eviction concurrency caps. Keep at or below the cluster's HyperConverged
	// live-migration limits so the descheduler never queues more migrations than
	// the cluster will run.
	EvictionNodeLimit  int `json:"evictionNodeLimit,omitempty"`
	EvictionTotalLimit int `json:"evictionTotalLimit,omitempty"`

	// InstallPSI also stages the worker PSI kernel-arg MachineConfig the
	// load-aware profile requires. Applying it REBOOTS every worker node, so it
	// is opt-in and never staged implicitly.
	InstallPSI bool `json:"installPSI,omitempty"`
}

// SoftTaint resolves the SoftTainter tri-state: nil means enabled. The one
// default living on a pointer (so Parse can distinguish an explicit false in a
// committed manifest from an omitted field), resolved here for every consumer.
func (s Spec) SoftTaint() bool {
	return s.SoftTainter == nil || *s.SoftTainter
}

// File is one rendered manifest: its platform-repo path plus a short name that
// identifies it in the draft (the ns/name-shaped draft keys need one).
type File struct {
	Name    string
	Path    string
	Content []byte
}

// withDefaults validates s and fills zero values.
func withDefaults(s Spec) (Spec, error) {
	switch s.Mode {
	case ModePredictive, ModeAutomatic:
	case "":
		return Spec{}, fmt.Errorf("a mode is required (Predictive or Automatic)")
	default:
		return Spec{}, fmt.Errorf("mode %q must be Predictive or Automatic", s.Mode)
	}
	if s.Threshold == "" {
		s.Threshold = defaultThreshold
	} else if !validThresholds[s.Threshold] {
		return Spec{}, fmt.Errorf("threshold %q must be AsymmetricLow, Low, Medium or High", s.Threshold)
	}
	if s.IntervalSeconds == 0 {
		s.IntervalSeconds = defaultInterval
	} else if s.IntervalSeconds < 10 || s.IntervalSeconds > 86400 {
		return Spec{}, fmt.Errorf("intervalSeconds must be 10..86400")
	}
	if s.EvictionNodeLimit == 0 {
		s.EvictionNodeLimit = defaultNodeLimit
	} else if s.EvictionNodeLimit < 1 || s.EvictionNodeLimit > 100 {
		return Spec{}, fmt.Errorf("evictionNodeLimit must be 1..100")
	}
	if s.EvictionTotalLimit == 0 {
		s.EvictionTotalLimit = defaultTotalLimit
	} else if s.EvictionTotalLimit < 1 || s.EvictionTotalLimit > 1000 {
		return Spec{}, fmt.Errorf("evictionTotalLimit must be 1..1000")
	}
	return s, nil
}

// Manifests renders the full DRS file set for the platform repo: the operator's
// namespace, OperatorGroup and Subscription (idempotent install scaffolding),
// the KubeDescheduler CR carrying the configuration, and — only when InstallPSI
// — the worker PSI MachineConfig.
func Manifests(s Spec) ([]File, error) {
	s, err := withDefaults(s)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, 5)
	for _, f := range []struct {
		name, path string
		obj        map[string]any
	}{
		{"namespace", NamespacePath, operatorNamespace()},
		{"operatorgroup", OperatorGroupPath, operatorGroup()},
		{"subscription", SubscriptionPath, subscription()},
		{"kubedescheduler", CRPath, kubeDescheduler(s)},
	} {
		out, err := yaml.Marshal(f.obj)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: f.name, Path: f.path, Content: out})
	}
	if s.InstallPSI {
		out, err := yaml.Marshal(psiMachineConfig())
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: "psi-machineconfig", Path: PSIPath, Content: out})
	}
	return files, nil
}

// operatorNamespace labels the install namespace into cluster monitoring so the
// descheduler's metrics (including Predictive-mode recommendations) are scraped.
func operatorNamespace() map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   Namespace,
			"labels": map[string]any{"openshift.io/cluster-monitoring": "true"},
		},
	}
}

// operatorGroup scopes the operator to its own namespace (own-namespace install
// mode, matching the console install flow).
func operatorGroup() map[string]any {
	return map[string]any{
		"apiVersion": "operators.coreos.com/v1",
		"kind":       "OperatorGroup",
		"metadata":   map[string]any{"name": Namespace, "namespace": Namespace},
		"spec":       map[string]any{"targetNamespaces": []any{Namespace}},
	}
}

// subscription installs the Kube Descheduler Operator from the Red Hat catalog.
// InstallPlan approval is Automatic so GitOps can install without a manual step.
func subscription() map[string]any {
	return map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata":   map[string]any{"name": "cluster-kube-descheduler-operator", "namespace": Namespace},
		"spec": map[string]any{
			"channel":             "stable",
			"name":                "cluster-kube-descheduler-operator",
			"source":              "redhat-operators",
			"sourceNamespace":     "openshift-marketplace",
			"installPlanApproval": "Automatic",
		},
	}
}

// kubeDescheduler is the configuration CR — a singleton named "cluster". The
// KubeVirtRelieveAndMigrate profile is the virt-specific load-aware rebalancer;
// PrometheusCPUCombined makes it decide on real measured load (PSI) rather than
// pod resource requests.
//
// The CR ships in the same sync as the Subscription that provides its CRD, and
// Argo's dry-run of a missing kind invalidates the whole operation — including
// that Subscription — deadlocking the enable. SkipDryRunOnMissingResource lets
// the install scaffolding apply; Argo then retries the CR until OLM registers
// the API.
func kubeDescheduler(s Spec) map[string]any {
	return map[string]any{
		"apiVersion": "operator.openshift.io/v1",
		"kind":       "KubeDescheduler",
		"metadata": map[string]any{
			"name":      "cluster",
			"namespace": Namespace,
			"annotations": map[string]any{
				"argocd.argoproj.io/sync-options": "SkipDryRunOnMissingResource=true",
			},
		},
		"spec": map[string]any{
			"managementState":             "Managed",
			"mode":                        s.Mode,
			"deschedulingIntervalSeconds": s.IntervalSeconds,
			"profiles":                    []any{"KubeVirtRelieveAndMigrate"},
			"profileCustomizations": map[string]any{
				"devActualUtilizationProfile": "PrometheusCPUCombined",
				"devDeviationThresholds":      s.Threshold,
				"devEnableSoftTainter":        s.SoftTaint(),
			},
			"evictionLimits": map[string]any{
				"node":  s.EvictionNodeLimit,
				"total": s.EvictionTotalLimit,
			},
		},
	}
}

// psiMachineConfig enables the PSI (Pressure Stall Information) kernel argument
// on workers — the load signal KubeVirtRelieveAndMigrate reads. The 99- prefix
// must sort after any 98-* MachineConfig so psi=1 wins.
func psiMachineConfig() map[string]any {
	return map[string]any{
		"apiVersion": "machineconfiguration.openshift.io/v1",
		"kind":       "MachineConfig",
		"metadata": map[string]any{
			"name":   "99-openshift-machineconfig-worker-psi-karg",
			"labels": map[string]any{"machineconfiguration.openshift.io/role": "worker"},
		},
		"spec": map[string]any{"kernelArguments": []any{"psi=1"}},
	}
}

// Parse reads a KubeDescheduler manifest (as committed by Manifests) back into
// the Spec it renders from — the GET view of the repo's current DRS config.
// Unknown or hand-edited fields outside the Spec surface are ignored.
func Parse(content []byte) (Spec, error) {
	var doc struct {
		Spec struct {
			Mode                        string `json:"mode"`
			DeschedulingIntervalSeconds int    `json:"deschedulingIntervalSeconds"`
			ProfileCustomizations       struct {
				DevDeviationThresholds string `json:"devDeviationThresholds"`
				DevEnableSoftTainter   *bool  `json:"devEnableSoftTainter"`
			} `json:"profileCustomizations"`
			EvictionLimits struct {
				Node  int `json:"node"`
				Total int `json:"total"`
			} `json:"evictionLimits"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return Spec{}, fmt.Errorf("parse KubeDescheduler: %w", err)
	}
	return Spec{
		Mode:               doc.Spec.Mode,
		Threshold:          doc.Spec.ProfileCustomizations.DevDeviationThresholds,
		IntervalSeconds:    doc.Spec.DeschedulingIntervalSeconds,
		SoftTainter:        doc.Spec.ProfileCustomizations.DevEnableSoftTainter,
		EvictionNodeLimit:  doc.Spec.EvictionLimits.Node,
		EvictionTotalLimit: doc.Spec.EvictionLimits.Total,
	}, nil
}
