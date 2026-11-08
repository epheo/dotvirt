// The words the UI says for the server's raw kinds. Policy kinds, trace
// stages and draft resource names are wire or persisted values (the draft's
// "drs" lives in users' drafts), never words a person reads: every surface
// that names one goes through here, and an unknown value reads as a neutral
// noun, never as itself.

export const POLICY_KIND_LABEL: Record<string, string> = {
	dfw: 'Project rule',
	admin: 'Cluster rule',
	baseline: 'Cluster default rule',
	gateway: 'Outbound rule',
	egressip: 'Fixed outbound IP',
	route: 'External route',
};

export const TRACE_STAGE_LABEL: Record<string, string> = {
	connectivity: 'Connectivity',
	segment: 'Network',
	admin: 'Cluster rule',
	dfw: 'Project rule',
	baseline: 'Cluster default rule',
	default: 'Default',
	gateway: 'Outbound rule',
	snat: 'Fixed outbound IP',
	route: 'External route',
};

// By draft resource name ('' is a VM, the draft's default).
export const DRAFT_RESOURCE_LABEL: Record<string, string> = {
	'': 'VM',
	vm: 'VM',
	network: 'Network',
	uplink: 'Uplink',
	namespace: 'Namespace',
	rolebinding: 'Tenant access',
	egressfirewall: 'Outbound rules',
	egressip: 'Fixed outbound IP',
	externalroute: 'External route',
	networkpolicy: 'Project rule',
	adminnetworkpolicy: 'Cluster rule',
	baselineadminnetworkpolicy: 'Cluster default rule',
	drs: 'Rebalancing',
	template: 'Template',
};

const own = (m: Record<string, string>, k: string) =>
	Object.prototype.hasOwnProperty.call(m, k) ? m[k] : undefined;

export const policyKindLabel = (kind: string): string => own(POLICY_KIND_LABEL, kind) ?? 'Rule';
export const traceStageLabel = (stage: string): string => own(TRACE_STAGE_LABEL, stage) ?? 'Step';
export const draftResourceLabel = (resource: string | undefined): string =>
	own(DRAFT_RESOURCE_LABEL, resource ?? '') ?? 'Object';
