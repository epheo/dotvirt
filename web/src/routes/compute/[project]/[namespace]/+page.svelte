<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '$lib/lenses';
	import { trailForScope } from '$lib/nav';
	import ContainerWorkspace from '$lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({
		kind: 'namespace',
		project: page.params.project!,
		namespace: page.params.namespace!
	});
</script>

<ContainerWorkspace {scope} trail={trailForScope(scope)} />
