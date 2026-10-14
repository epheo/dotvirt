<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '#lib/lenses.js';
	import { trailForScope } from '#lib/nav.js';
	import ContainerWorkspace from '#lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({ kind: 'project', project: page.params.project! });
</script>

<ContainerWorkspace {scope} section="compute" trail={trailForScope(scope)} />
