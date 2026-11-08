import {
	api,
	type HostCapacity,
	type HostLoad,
	type Node,
	type NodeInfo,
	type NodeTarget,
} from '#lib/api.js';
import { metricsGap } from '#lib/metrics.js';
import { resource, type Resource } from '#lib/resource.svelte.js';

// The host roster behind every host picker, pulled fresh per dialog open
// (readiness and maintenance move). Listing nodes is cluster-scoped RBAC: a
// caller without it reads `failed`, and the picker degrades (scheduler-placed
// migration, free-text pins) instead of painting an error.
export function nodeTargets(): Resource<NodeTarget[]> {
	return resource<NodeTarget[]>(
		() => '',
		() => api.nodes(),
	);
}

export type Room = { cap: HostCapacity | null; nodes: Node[] | null };

// Host capacity and node state, read only while a memory size is being
// chosen. Capacity is metrics-backed: with metrics off cap is null and the
// memory hint stays silent; without node rights every host counts.
export function memoryRoom(active: () => boolean): Resource<Room | null> {
	return resource<Room | null>(
		() => (active() ? 'room' : ''),
		async (k) => {
			if (!k) return null;
			const [cap, nodes] = await Promise.all([
				api
					.capacity()
					.then((c) => (metricsGap(c.unavailable) ? null : c))
					.catch(() => null),
				api.nodes().catch(() => null),
			]);
			return { cap, nodes };
		},
	);
}

export type HostFacts = {
	nodes: Resource<Node[] | null>;
	cap: Resource<HostCapacity | null>;
	load: Resource<HostLoad | null>;
	info: Resource<NodeInfo | null>;
};

// What a Hosts page reads about its hosts, once per page, for its header and
// its Summary alike: node state, capacity, live load, and on a host's page
// that host's own read (whether the caller may cordon it). on() is false
// outside the Hosts section, where nothing is read. The metrics feeds poll
// only while live() says something shows them. A host switch reads the node
// list again, so the header's state is that of the host now in view.
export function hostFacts(opts: {
	on: () => boolean;
	node: () => string;
	live: () => boolean;
}): HostFacts {
	const idle = () => Promise.resolve(null);
	const poll = () => (opts.on() && opts.live() ? 30_000 : 0);
	return {
		nodes: resource<Node[] | null>(
			() => (opts.on() ? `nodes|${opts.node()}` : ''),
			(k) => (k ? api.nodes() : idle()),
		),
		cap: resource<HostCapacity | null>(
			() => (opts.on() ? 'capacity' : ''),
			(k) => (k ? api.capacity() : idle()),
			{ poll },
		),
		load: resource<HostLoad | null>(
			() => (opts.on() && !opts.node() ? 'load' : ''),
			(k) => (k ? api.hostLoad() : idle()),
			{ poll },
		),
		info: resource<NodeInfo | null>(
			() => (opts.on() ? opts.node() : ''),
			(k) => (k ? api.nodeInfo(k) : idle()),
			{ reset: true },
		),
	};
}
