// The model behind Networking's Map tab: lanes from the outside world down to
// projects, aggregated (counts, top-N, "N more" groups) so a platform of a few
// thousand VMs never draws one node per VM. Pure functions over the inventory
// and the networking catalog; the components only lay out what this returns.
import type { NIC, Network, Policy, Project, Uplink, VM } from '#lib/api.js';
import { NO_NETWORK, POD_NETWORK, vmNetworkKeys } from '#lib/lenses.js';
import { networkKey, resolveNIC } from '#lib/networks.js';
import { canMeet, contains, overlaps } from '#lib/subnets.js';

// --- membership ---------------------------------------------------------------

export interface Membership {
	byNet: Map<string, VM[]>; // network key (or POD_NETWORK) -> its VMs, each once
	podOnly: number; // VMs whose only network is the pod network
}

// A VM with no NIC in the file still gets the pod interface KubeVirt attaches.
const IMPLICIT: NIC[] = [{ name: 'default', network: 'pod', implicit: true }];

/** Which VMs sit on which network, through the same keys the tree groups by. */
export function membership(vms: VM[], networks: Network[]): Membership {
	const byNet = new Map<string, VM[]>();
	let podOnly = 0;
	for (const vm of vms) {
		let keys = vmNetworkKeys(vm, networks);
		if (keys.length === 1 && keys[0] === NO_NETWORK)
			keys = vmNetworkKeys({ ...vm, networks: IMPLICIT }, networks);
		if (keys.length === 1 && keys[0] === POD_NETWORK) podOnly++;
		for (const k of keys) {
			const list = byNet.get(k);
			if (list) list.push(vm);
			else byNet.set(k, [vm]);
		}
	}
	return { byNet, podOnly };
}

// --- problems -----------------------------------------------------------------

export interface NetProblem {
	id: string;
	kind: 'uplink' | 'overlap';
	severity: 'danger' | 'warn';
	title: string; // the badge: "Fails on host-41", "Overlap"
	detail: string; // one sentence
	uplink?: string;
	policy?: string; // the uplink's policy, when one object declares it
	hosts?: string[];
	reason?: string;
	networks?: string[]; // keys
}

/** The builtin uplink reads as "default", whatever bridge mapping backs it. */
export const uplinkLabel = (u: Uplink): string => (u.builtin ? 'default' : u.name);

/**
 * The standing networking problems: uplinks that failed on some hosts, and
 * networks whose subnets overlap where they can meet (subnets.canMeet). VM
 * membership answers "a VM already sits on both".
 */
export function networkProblems(
	networks: Network[],
	uplinks: Uplink[],
	members?: Membership,
): NetProblem[] {
	const out: NetProblem[] = [];
	for (const u of uplinks) {
		const hosts = (u.failures ?? []).map((f) => f.node);
		if (!hosts.length) continue;
		const reason = u.failures![0].reason;
		out.push({
			id: `uplink:${u.name}`,
			kind: 'uplink',
			severity: 'danger',
			title: hosts.length === 1 ? `Fails on ${hosts[0]}` : `Fails on ${hosts.length} hosts`,
			detail: `${uplinkLabel(u)} is not configured on ${list(hosts)}: ${reason}.`,
			uplink: u.name,
			policy: u.policy,
			hosts,
			reason,
		});
	}
	for (const group of overlapGroups(networks, members)) {
		const names = group.map((n) => n.name);
		const cidrs = new Set(group.flatMap((n) => n.subnets ?? []));
		const detail =
			cidrs.size === 1
				? `${list(names)} ${group.length === 2 ? 'both' : 'all'} use ${[...cidrs][0]}.`
				: `${list(group.map((n) => `${n.name} (${(n.subnets ?? []).join(', ')})`))} overlap.`;
		out.push({
			id: `overlap:${group.map(networkKey).join(',')}`,
			kind: 'overlap',
			severity: 'warn',
			title: 'Overlap',
			detail,
			networks: group.map(networkKey),
		});
	}
	return out;
}

function overlapGroups(networks: Network[], members?: Membership): Network[][] {
	const nets = networks.filter((n) => n.subnets?.length);
	const parent = nets.map((_, i) => i);
	const find = (i: number): number => (parent[i] === i ? i : (parent[i] = find(parent[i])));
	const sharesVM = (a: Network, b: Network) => {
		const on = new Set(members?.byNet.get(networkKey(a)) ?? []);
		return (members?.byNet.get(networkKey(b)) ?? []).some((v) => on.has(v));
	};
	for (let i = 0; i < nets.length; i++)
		for (let j = i + 1; j < nets.length; j++)
			if (
				nets[i].subnets!.some((a) => nets[j].subnets!.some((b) => overlaps(a, b))) &&
				canMeet(nets[i], nets[j], sharesVM(nets[i], nets[j]))
			)
				parent[find(i)] = find(j);
	const groups = new Map<number, Network[]>();
	nets.forEach((n, i) => {
		const r = find(i);
		groups.set(r, [...(groups.get(r) ?? []), n]);
	});
	return [...groups.values()]
		.filter((g) => g.length > 1)
		.map((g) => g.sort((a, b) => a.name.localeCompare(b.name)));
}

/** Hosts where a VLAN network is cut off because its uplink failed there. */
export function cutOffHosts(net: Network, uplinks: Uplink[]): string[] {
	if (net.kind !== 'vlan' || !net.uplink) return [];
	return (uplinks.find((u) => u.name === net.uplink)?.failures ?? []).map((f) => f.node);
}

const list = (xs: string[]): string =>
	xs.length < 2 ? (xs[0] ?? '') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`;

// --- the map ------------------------------------------------------------------

export type MapFocus =
	| { kind: 'vm'; vm: VM }
	| { kind: 'network'; key: string }
	| { kind: 'uplink'; name: string }
	| { kind: 'project'; name: string };

export interface NetNode {
	key: string;
	net: Network;
	vms: number;
	problems: NetProblem[];
	cutOff: string[]; // hosts the network cannot reach
}

export interface UplinkRow {
	uplink: Uplink;
	label: string;
	known: boolean; // false when the caller cannot read the fabric: only the name is known
	allHosts: boolean;
	problem: NetProblem | null;
	nets: NetNode[];
}

export interface ProjectNode {
	name: string;
	vms: number;
	networks: number;
	problems: NetProblem[];
}

export interface ClusterMap {
	edge: { fixedIPs: number; routes: number };
	podOnly: number;
	rows: UplinkRow[];
	moreNetworks: { count: number; vms: number; shared: number; vlan: number } | null;
	projects: ProjectNode[];
	moreProjects: { count: number; vms: number; networks: number; problems: NetProblem[] } | null;
	shown: { networks: number; networksTotal: number; projects: number; projectsTotal: number };
}

export interface MapInput {
	networks: Network[];
	uplinks: Uplink[];
	projects: Project[];
	policies: Policy[];
	members: Membership;
	problems: NetProblem[];
	focus?: MapFocus | null;
	problemsOnly?: boolean;
	netBudget?: number;
	projectBudget?: number;
}

/** Networks the shared lane draws: shared and VLAN ones (project networks sit in their project). */
const laneNetwork = (n: Network): boolean => n.scope === 'shared' || n.kind === 'vlan';

/**
 * The cluster map: every uplink as a row, the shared and VLAN networks beside
 * their uplink (each uplink's biggest, every problem and the focus first, then
 * the busiest up to the budget), and the biggest projects; the rest fold into
 * "N more" groups that still carry their problems.
 */
export function clusterMap(input: MapInput): ClusterMap {
	const { networks, members, problems, focus, problemsOnly } = input;
	const netBudget = problemsOnly ? 0 : (input.netBudget ?? 6);
	const projectBudget = problemsOnly ? 0 : (input.projectBudget ?? 5);
	const vmsOn = (key: string) => members.byNet.get(key)?.length ?? 0;
	const problemsOf = (key: string) => problems.filter((p) => p.networks?.includes(key));

	// Rows: the builtin uplink first, then by name. A caller without fabric
	// access still sees the uplinks its VLAN networks name.
	const uplinks = [...input.uplinks];
	const known = new Set(uplinks.map((u) => u.name));
	if (!uplinks.some((u) => u.builtin))
		uplinks.push({ name: 'default', bridge: '', builtin: true, nodeCount: 0 });
	for (const n of networks)
		if (n.kind === 'vlan' && n.uplink && !uplinks.some((u) => u.name === n.uplink))
			uplinks.push({ name: n.uplink, bridge: '', nodeCount: 0 });
	uplinks.sort((a, b) => Number(!!b.builtin) - Number(!!a.builtin) || a.name.localeCompare(b.name));
	const hostCount = uplinks.find((u) => u.builtin)?.nodeCount ?? 0;
	const rowOf = (n: Network): string =>
		n.kind === 'vlan' && n.uplink ? n.uplink : uplinks.find((u) => u.builtin)!.name;

	const lane = networks.filter(laneNetwork);
	const node = (net: Network): NetNode => {
		const key = networkKey(net);
		return {
			key,
			net,
			vms: vmsOn(key),
			problems: problemsOf(key),
			cutOff: cutOffHosts(net, input.uplinks),
		};
	};
	const all = lane.map(node).sort((a, b) => b.vms - a.vms || a.key.localeCompare(b.key));

	const picked = new Set<string>();
	const focusNets = new Set<string>();
	if (focus?.kind === 'network') focusNets.add(focus.key);
	if (focus?.kind === 'vm') for (const k of vmNetworkKeys(focus.vm, networks)) focusNets.add(k);
	for (const n of all)
		if (n.problems.length || n.cutOff.length || focusNets.has(n.key)) picked.add(n.key);
	if (!problemsOnly)
		for (const u of uplinks) {
			const biggest = all.find((n) => rowOf(n.net) === u.name);
			if (biggest) picked.add(biggest.key);
		}
	for (const n of all) if (picked.size < netBudget) picked.add(n.key);

	const rows: UplinkRow[] = uplinks.map((u) => ({
		uplink: u,
		label: uplinkLabel(u),
		known: known.has(u.name),
		allHosts: hostCount > 0 && u.nodeCount >= hostCount,
		problem: problems.find((p) => p.kind === 'uplink' && p.uplink === u.name) ?? null,
		nets: all.filter((n) => picked.has(n.key) && rowOf(n.net) === u.name),
	}));
	const rest = all.filter((n) => !picked.has(n.key));

	// Projects: by VMs, the focus's project forced in.
	const projectOfNS = new Map<string, string>();
	for (const p of input.projects)
		for (const ns of p.namespaces) projectOfNS.set(ns.namespace, p.name);
	const ownNets = (p: Project) => {
		const nss = new Set(p.namespaces.map((n) => n.namespace));
		return networks.filter((n) => !laneNetwork(n) && n.namespace && nss.has(n.namespace));
	};
	const pnodes: ProjectNode[] = input.projects
		.map((p) => {
			const own = ownNets(p);
			return {
				name: p.name,
				vms: p.namespaces.reduce((s, n) => s + n.vms.length, 0),
				networks: own.length,
				problems: problems.filter((pr) => own.some((n) => pr.networks?.includes(networkKey(n)))),
			};
		})
		.sort((a, b) => b.vms - a.vms || a.name.localeCompare(b.name));
	const forced = new Set<string>();
	if (focus?.kind === 'project') forced.add(focus.name);
	if (focus?.kind === 'vm') forced.add(projectOfNS.get(focus.vm.namespace) ?? '');
	if (focus?.kind === 'network') {
		const n = networks.find((x) => networkKey(x) === focus.key);
		if (n?.namespace) forced.add(projectOfNS.get(n.namespace) ?? '');
	}
	if (problemsOnly) for (const p of pnodes) if (p.problems.length) forced.add(p.name);
	const shownProjects = new Set(pnodes.filter((p) => forced.has(p.name)).map((p) => p.name));
	for (const p of pnodes) if (shownProjects.size < projectBudget) shownProjects.add(p.name);
	const projects = pnodes.filter((p) => shownProjects.has(p.name));
	const restProjects = pnodes.filter((p) => !shownProjects.has(p.name));

	return {
		edge: {
			fixedIPs: input.policies.filter((p) => p.kind === 'egressip').length,
			routes: input.policies.filter((p) => p.kind === 'route').length,
		},
		podOnly: members.podOnly,
		rows,
		moreNetworks:
			rest.length && !problemsOnly
				? {
						count: rest.length,
						vms: rest.reduce((s, n) => s + n.vms, 0),
						shared: rest.filter((n) => n.net.kind !== 'vlan').length,
						vlan: rest.filter((n) => n.net.kind === 'vlan').length,
					}
				: null,
		projects,
		moreProjects:
			restProjects.length && !problemsOnly
				? {
						count: restProjects.length,
						vms: restProjects.reduce((s, p) => s + p.vms, 0),
						networks: restProjects.reduce((s, p) => s + p.networks, 0),
						problems: uniq(restProjects.flatMap((p) => p.problems)),
					}
				: null,
		shown: {
			networks: picked.size,
			networksTotal: all.length,
			projects: projects.length,
			projectsTotal: pnodes.length,
		},
	};
}

const uniq = (ps: NetProblem[]): NetProblem[] => [...new Map(ps.map((p) => [p.id, p])).values()];

// --- search -------------------------------------------------------------------

export interface MapSearch {
	vms: VM[];
	networks: Network[];
	total: number;
	summary: string; // "1 VM", "3 VMs, 1 network", "No match"
}

const plural = (n: number, one: string) =>
	`${n.toLocaleString('en-US')} ${one}${n === 1 ? '' : 's'}`;

/**
 * Finds VMs by name or address and networks by name, subnet or an address
 * inside it. Exact names rank first; lists are capped at limit, the summary
 * counts everything.
 */
export function searchMap(q: string, vms: VM[], networks: Network[], limit = 20): MapSearch | null {
	const s = q.trim().toLowerCase();
	if (!s) return null;
	const ipish = /^[0-9a-f.:]+$/.test(s) && /[.:]/.test(s);
	const vmHits = vms.filter(
		(v) =>
			v.name.toLowerCase().includes(s) ||
			(ipish &&
				[v.guestIP, ...(v.ips ?? []), ...(v.networks ?? []).map((n) => n.ip)].some((ip) =>
					ip?.startsWith(s),
				)),
	);
	const netHits = networks.filter(
		(n) =>
			n.name.toLowerCase().includes(s) ||
			(n.subnets ?? []).some((c) => c.startsWith(s) || (ipish && contains(c, s))),
	);
	const exactFirst = <T extends { name: string }>(xs: T[]) =>
		[...xs].sort((a, b) => Number(b.name.toLowerCase() === s) - Number(a.name.toLowerCase() === s));
	const parts = [
		vmHits.length ? plural(vmHits.length, 'VM') : '',
		netHits.length ? plural(netHits.length, 'network') : '',
	].filter(Boolean);
	return {
		vms: exactFirst(vmHits).slice(0, limit),
		networks: exactFirst(netHits).slice(0, limit),
		total: vmHits.length + netHits.length,
		summary: parts.length ? parts.join(', ') : 'No match',
	};
}

// --- the way out --------------------------------------------------------------

export interface WayStep {
	kind: 'network' | 'uplink' | 'edge' | 'internet' | 'fabric' | 'isolated';
	text: string;
}

export interface WayOut {
	steps: WayStep[];
	via: 'edge' | 'fabric' | 'none';
	network: string; // the key the path starts on (POD_NETWORK for the pod network)
	uplink: string; // the uplink it leaves through, '' when none
	host: string; // where the VM runs, '' when stopped
	also: Network[]; // VLAN networks the VM reaches the fabric on besides the path
}

/**
 * How a VM's traffic leaves the cluster. The pod interface (or the project's
 * primary network behind it) leaves through the default uplink and the
 * cluster edge, where the source address becomes the host's unless a fixed
 * outbound IP or an external route binds the VM (egress, from the evaluator).
 * A VLAN network leaves through its uplink to the datacenter fabric with the
 * VM's own address. Anything else is an isolated network with no way out.
 */
export function wayOut(
	vm: VM,
	networks: Network[],
	uplinks: Uplink[],
	egress: { snat?: Policy; route?: Policy } = {},
): WayOut {
	const nics = vm.networks?.length ? vm.networks : IMPLICIT;
	const host = vm.nodeName ?? '';
	const on = host ? ` on ${host}` : '';
	const resolved = nics.map((nic) => ({ nic, net: resolveNIC(nic, vm.namespace, networks) }));
	const vlans = resolved.filter((r) => r.net?.kind === 'vlan').map((r) => r.net!);
	const builtin = uplinks.find((u) => u.builtin);
	const pod = resolved.find((r) => !r.nic.network || r.nic.network === 'pod');

	if (pod) {
		let edge = host
			? `Cluster edge. Source IP becomes ${host}'s.`
			: "Cluster edge. Source IP becomes its host's.";
		const snatIPs = egress.snat?.rules?.[0]?.peer;
		const hop = egress.route?.rules?.[0]?.peer?.replace(/^via /, '');
		if (egress.snat && snatIPs)
			edge = `Cluster edge. Source IP becomes ${snatIPs}, fixed outbound IP ${egress.snat.name}.`;
		else if (egress.route && hop)
			edge = `Cluster edge. Leaves through ${hop}, external route ${egress.route.name}.`;
		return {
			steps: [
				{ kind: 'network', text: pod.net ? pod.net.name : POD_NETWORK },
				{ kind: 'uplink', text: `default uplink${on}` },
				{ kind: 'edge', text: edge },
				{ kind: 'internet', text: 'Internet' },
			],
			via: 'edge',
			network: pod.net ? networkKey(pod.net) : POD_NETWORK,
			uplink: builtin?.name ?? 'default',
			host,
			also: vlans,
		};
	}
	if (vlans.length) {
		const v = vlans[0];
		return {
			steps: [
				{ kind: 'network', text: v.vlan ? `${v.name}, VLAN ${v.vlan}` : v.name },
				{ kind: 'uplink', text: `${v.uplink ?? 'its'} uplink${on}` },
				{ kind: 'fabric', text: 'Datacenter fabric. The VM keeps its own IP.' },
			],
			via: 'fabric',
			network: networkKey(v),
			uplink: v.uplink ?? '',
			host,
			also: vlans.slice(1),
		};
	}
	const first = resolved.find((r) => r.net)?.net;
	const name = first?.name ?? nics[0]?.network ?? 'its network';
	return {
		steps: [
			{ kind: 'network', text: name },
			{ kind: 'isolated', text: `No way out. ${name} reaches only the VMs on it.` },
		],
		via: 'none',
		network: first ? networkKey(first) : '',
		uplink: '',
		host,
		also: [],
	};
}
