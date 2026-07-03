<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '$lib/lenses';
	import { trailForScope } from '$lib/nav';
	import ContainerWorkspace from '$lib/workspace/ContainerWorkspace.svelte';

	// Rest param: network keys may carry a raw "ns/name" NAD ref.
	const scope = $derived<Scope>({ kind: 'network', network: page.params.segment! });
</script>

<ContainerWorkspace {scope} trail={trailForScope(scope)} />
