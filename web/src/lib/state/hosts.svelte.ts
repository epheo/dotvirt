import { api, type NodeTarget } from '$lib/api';
import { resource, type Resource } from '$lib/resource.svelte';

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
