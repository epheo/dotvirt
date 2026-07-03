<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '$lib/lenses';
	import { trailForScope } from '$lib/nav';
	import ContainerWorkspace from '$lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({ kind: 'project', project: page.params.project! });
</script>

<ContainerWorkspace {scope} trail={trailForScope(scope)} />
