import { api, type HostCapacity, type Node, type NodeTarget } from '#lib/api.js';
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
