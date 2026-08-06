package model

// The security plane: DFW tiers, gateway/Tier-0 policies, and the trace.

// PolicyKind classifies a firewall/routing policy by the tier the UI
// presents: the east-west Distributed Firewall (per-project NetworkPolicy and
// its cluster-wide admin/baseline overrides), the per-project Gateway Firewall
// (EgressFirewall on the Tier-1), and the Tier-0 planes (EgressIP SNAT,
// policy-based external routes).
type PolicyKind string

const (
	PolicyDFW      PolicyKind = "dfw"      // NetworkPolicy - project east-west rules
	PolicyAdmin    PolicyKind = "admin"    // AdminNetworkPolicy - cluster-wide, priority-ordered
	PolicyBaseline PolicyKind = "baseline" // BaselineAdminNetworkPolicy - the cluster default
	PolicyGateway  PolicyKind = "gateway"  // EgressFirewall - project north-south egress
	PolicyEgressIP PolicyKind = "egressip" // EgressIP - Tier-0 source-NAT pool
	PolicyRoute    PolicyKind = "route"    // AdminPolicyBasedExternalRoute - Tier-0 next hop
)

// PolicyRuleView is one rule row as the Security view shows it: direction,
// action, a human peer summary, and a compact port list. Summaries, not the
// spec - editing goes through git, the view only has to read well.
type PolicyRuleView struct {
	Direction string `json:"direction"`       // Ingress | Egress
	Action    string `json:"action"`          // Allow | Deny | Pass
	Peer      string `json:"peer,omitempty"`  // source/destination summary; empty = any
	Ports     string `json:"ports,omitempty"` // "TCP/443, UDP/53"; empty = any
}

// Policy is one live firewall/routing object rendered in security vocabulary,
// with the same per-object ArgoCD drift surface networks and VMs carry.
type Policy struct {
	Name      string     `json:"name"`
	Kind      PolicyKind `json:"kind"`
	Namespace string     `json:"namespace,omitempty"` // empty for cluster-scoped kinds
	Backing   string     `json:"backing"`             // the Kubernetes kind behind the row
	Priority  int        `json:"priority,omitempty"`  // ANP precedence (lower wins)
	Target    string     `json:"target,omitempty"`    // what the policy applies to, summarized
	// Namespaces a cluster-scoped policy provably pins to (the metadata.name
	// selector netgen writes). Nil when the selector isn't enumerable - a tenant
	// filter must then keep the row rather than hide a possibly-applying rule.
	Namespaces []string         `json:"namespaces,omitempty"`
	Rules      []PolicyRuleView `json:"rules,omitempty"`

	Sync      SyncStatus `json:"sync,omitempty"`
	Health    string     `json:"health,omitempty"`
	SyncError string     `json:"syncError,omitempty"`
}

// PolicyInventory is GET /api/policies: the policies the caller may see -
// namespace-scoped kinds in visible namespaces, cluster-scoped kinds only for
// callers with the matching platform authoring authority.
type PolicyInventory struct {
	Policies []Policy `json:"policies"`
}

// PolicyBinding is one policy bound to a workload in the effective-policy
// answer: the rendered policy plus how certain the match is.
type PolicyBinding struct {
	Policy Policy `json:"policy"`
	// Conditional: a pod-level selector couldn't be resolved (namespace-scoped
	// query, or an unreadable selector), so the policy applies only to the pods
	// matching its target - the binding is kept rather than hidden.
	Conditional bool `json:"conditional,omitempty"`
	// Note carries tier semantics the list order alone can't express (e.g. the
	// baseline tier applies only where nothing above decided).
	Note string `json:"note,omitempty"`
}

// EffectivePolicy answers "what governs this workload, in evaluation order":
// the east-west chain (admin tiers by precedence, then the project rules that
// select it, then baseline) and the egress planes (gateway firewall, SNAT,
// external routes). Control-plane binding, not flow simulation: it shows which
// policies bind and in what order, it does not verdict a specific connection.
type EffectivePolicy struct {
	Namespace string `json:"namespace"`
	VM        string `json:"vm,omitempty"`
	// Labels pod selectors were matched against. Live: the running VMI's (what
	// the virt-launcher pod carries); otherwise the manifest template's.
	Labels     map[string]string `json:"labels,omitempty"`
	LabelsLive bool              `json:"labelsLive,omitempty"`

	EastWest []PolicyBinding `json:"eastWest,omitempty"`
	// Selected by >=1 NetworkPolicy for the direction, so everything the
	// project tier doesn't explicitly allow is denied there.
	DefaultDenyIngress bool `json:"defaultDenyIngress,omitempty"`
	DefaultDenyEgress  bool `json:"defaultDenyEgress,omitempty"`

	Gateway []PolicyBinding `json:"gateway,omitempty"`
	SNAT    []PolicyBinding `json:"snat,omitempty"`
	Routes  []PolicyBinding `json:"routes,omitempty"`
}

// TraceRequest simulates one flow: a source VM, a destination (an in-cluster
// VM or an external IP), and the protocol/port. Control-plane simulation of
// the live policy objects - no packet is injected.
type TraceRequest struct {
	Source      TraceEndpointRef `json:"source"`
	Destination TraceEndpointRef `json:"destination"`
	Protocol    string           `json:"protocol,omitempty"` // TCP (default) | UDP | SCTP
	Port        int              `json:"port,omitempty"`     // 0 = any port
}

// TraceEndpointRef is one end of a simulated flow: a VM, or (destination only)
// a bare address outside the cluster.
type TraceEndpointRef struct {
	Namespace string `json:"namespace,omitempty"`
	VM        string `json:"vm,omitempty"`
	IP        string `json:"ip,omitempty"`
}

// TraceResult is the simulation's answer: the overall verdict and every
// observation on the path, in evaluation order. Deny is only ever certain: an
// unresolved rule (named port, a stopped VM's unknown addresses, a DNS rule)
// downgrades the verdict to Conditional instead of guessing.
type TraceResult struct {
	Verdict string      `json:"verdict"` // Allow | Deny | Conditional | Unreachable
	Steps   []TraceStep `json:"steps"`
}

// TraceStep is one observation: the stage, the (possibly) deciding policy and
// rule, and its outcome. Decisive marks the rule that fixed a direction's
// verdict; Conditional keeps a maybe-matching rule visible rather than hidden.
type TraceStep struct {
	Stage     string          `json:"stage"`               // connectivity | segment | admin | dfw | baseline | default | gateway | snat | route
	Direction string          `json:"direction,omitempty"` // Egress (source side) | Ingress (destination side)
	Policy    *Policy         `json:"policy,omitempty"`
	Rule      *PolicyRuleView `json:"rule,omitempty"`
	// Action: Allow | Deny | Pass for rule stages; Reachable | Unreachable |
	// Bypass for connectivity; SNAT | Route for the informational planes.
	Action      string `json:"action"`
	Conditional bool   `json:"conditional,omitempty"`
	Decisive    bool   `json:"decisive,omitempty"`
	Note        string `json:"note,omitempty"`
}
