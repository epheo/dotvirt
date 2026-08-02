// Package hagen renders the Medik8s Node Health Check manifests behind
// dotvirt's HA panel - automatic host fencing + VM restart on host failure,
// the vSphere HA analog - from a small spec, the way drsgen renders DRS.
// Owns-nothing: the output is proposed via PR into the platform repo and
// applied by Argo, never written to the cluster. All output paths are
// constants (no user input ever becomes a path segment) and every field is
// range-validated.
package hagen

import (
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// Namespace is the install namespace Medik8s documents for its operators
// (Node Health Check, Self Node Remediation, Fence Agents Remediation).
const Namespace = "openshift-workload-availability"

// CRName names the NodeHealthCheck dotvirt manages. The CR is cluster-scoped;
// "workers" states its blast radius - control-plane nodes are excluded (see
// nodeHealthCheck).
const CRName = "workers"

// Platform-repo paths. CRPath is the NodeHealthCheck CR: its presence on the
// base branch is what "HA is configured" means, and it is the one file a
// disable removes (the operator install stays).
const (
	NamespacePath       = "ha/namespace.yaml"
	OperatorGroupPath   = "ha/operatorgroup.yaml"
	SubscriptionPath    = "ha/subscription.yaml"
	SNRSubscriptionPath = "ha/subscription-snr.yaml"
	CRPath              = "ha/nodehealthcheck.yaml"
)

// Defaults applied when a field is zero: the operator's own stock values -
// 5 minutes of unresponsiveness before fencing, remediation halted below a
// healthy majority.
const (
	defaultUnhealthySeconds  = 300
	defaultMinHealthyPercent = 51
)

// Spec describes the HA configuration to render. Zero values take the
// defaults above.
type Spec struct {
	// UnhealthySeconds is how long a worker must stay NotReady/Unknown before
	// remediation begins - the detection patience. Too short fences hosts over
	// transient network blips; the floor guards against that.
	UnhealthySeconds int `json:"unhealthySeconds,omitempty"`

	// MinHealthyPercent halts remediation when fewer than this share of
	// observed workers are healthy - the storm brake against fencing a whole
	// cluster over a shared failure (network partition, apiserver outage).
	MinHealthyPercent int `json:"minHealthyPercent,omitempty"`
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
	if s.UnhealthySeconds == 0 {
		s.UnhealthySeconds = defaultUnhealthySeconds
	} else if s.UnhealthySeconds < 60 || s.UnhealthySeconds > 3600 {
		return Spec{}, fmt.Errorf("unhealthySeconds must be 60..3600")
	}
	if s.MinHealthyPercent == 0 {
		s.MinHealthyPercent = defaultMinHealthyPercent
	} else if s.MinHealthyPercent < 1 || s.MinHealthyPercent > 100 {
		return Spec{}, fmt.Errorf("minHealthyPercent must be 1..100")
	}
	return s, nil
}

// Manifests renders the full HA file set for the platform repo: the operator's
// namespace, OperatorGroup and Subscription (idempotent install scaffolding)
// and the NodeHealthCheck CR carrying the configuration.
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
		{"subscription", SubscriptionPath, subscription("node-healthcheck-operator")},
		{"subscription-snr", SNRSubscriptionPath, subscription("self-node-remediation")},
		{"nodehealthcheck", CRPath, nodeHealthCheck(s)},
	} {
		out, err := yaml.Marshal(f.obj)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: f.name, Path: f.path, Content: out})
	}
	return files, nil
}

func operatorNamespace() map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": Namespace},
	}
}

// operatorGroup has no targetNamespaces: the Node Health Check Operator
// installs in all-namespaces mode (its CSV supports nothing narrower).
func operatorGroup() map[string]any {
	return map[string]any{
		"apiVersion": "operators.coreos.com/v1",
		"kind":       "OperatorGroup",
		"metadata":   map[string]any{"name": "workload-availability", "namespace": Namespace},
	}
}

// subscription installs one Medik8s operator. Node Health Check and Self Node
// Remediation each get an explicit Subscription: NHC's OLM dependency on the
// SNR template API does not resolve on every catalog (observed live - the
// NodeHealthCheck then sits on a missing SelfNodeRemediationTemplate CRD), and
// an explicit pair is deterministic everywhere while staying a no-op where the
// dependency would have resolved.
func subscription(pkg string) map[string]any {
	return map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata":   map[string]any{"name": pkg, "namespace": Namespace},
		"spec": map[string]any{
			"channel":             "stable",
			"name":                pkg,
			"source":              "redhat-operators",
			"sourceNamespace":     "openshift-marketplace",
			"installPlanApproval": "Automatic",
		},
	}
}

// nodeHealthCheck is the configuration CR. Control-plane nodes are excluded:
// fencing one risks etcd quorum, and their availability is the platform's
// problem, not the VM fleet's. The remediator is SNR's resource-deletion
// template - it deletes the dead node's pods AND VolumeAttachments, which is
// what lets KubeVirt restart a VM whose RWO volume was attached to the lost
// host.
//
// The CR ships in the same sync as the Subscription that provides its CRD, and
// Argo's dry-run of a missing kind invalidates the whole operation - including
// that Subscription - deadlocking the enable. SkipDryRunOnMissingResource lets
// the install scaffolding apply; Argo then retries the CR until OLM registers
// the API.
func nodeHealthCheck(s Spec) map[string]any {
	duration := fmt.Sprintf("%ds", s.UnhealthySeconds)
	return map[string]any{
		"apiVersion": "remediation.medik8s.io/v1alpha1",
		"kind":       "NodeHealthCheck",
		"metadata": map[string]any{
			"name": CRName,
			"annotations": map[string]any{
				"argocd.argoproj.io/sync-options": "SkipDryRunOnMissingResource=true",
			},
		},
		"spec": map[string]any{
			"minHealthy": fmt.Sprintf("%d%%", s.MinHealthyPercent),
			"selector": map[string]any{
				"matchExpressions": []any{
					map[string]any{"key": "node-role.kubernetes.io/master", "operator": "DoesNotExist"},
					map[string]any{"key": "node-role.kubernetes.io/control-plane", "operator": "DoesNotExist"},
				},
			},
			"remediationTemplate": map[string]any{
				"apiVersion": "self-node-remediation.medik8s.io/v1alpha1",
				"kind":       "SelfNodeRemediationTemplate",
				"namespace":  Namespace,
				"name":       "self-node-remediation-resource-deletion-template",
			},
			"unhealthyConditions": []any{
				map[string]any{"type": "Ready", "status": "False", "duration": duration},
				map[string]any{"type": "Ready", "status": "Unknown", "duration": duration},
			},
		},
	}
}

// Parse reads a NodeHealthCheck manifest (as committed by Manifests) back into
// the Spec it renders from - the GET view of the repo's current HA config.
// Unknown or hand-edited fields outside the Spec surface are ignored.
func Parse(content []byte) (Spec, error) {
	var doc struct {
		Spec struct {
			MinHealthy          string `json:"minHealthy"`
			UnhealthyConditions []struct {
				Duration string `json:"duration"`
			} `json:"unhealthyConditions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return Spec{}, fmt.Errorf("parse NodeHealthCheck: %w", err)
	}
	var out Spec
	if pct, err := strconv.Atoi(strings.TrimSuffix(doc.Spec.MinHealthy, "%")); err == nil {
		out.MinHealthyPercent = pct
	}
	if len(doc.Spec.UnhealthyConditions) > 0 {
		if sec, err := strconv.Atoi(strings.TrimSuffix(doc.Spec.UnhealthyConditions[0].Duration, "s")); err == nil {
			out.UnhealthySeconds = sec
		}
	}
	return out, nil
}
