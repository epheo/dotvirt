<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '#lib/lenses.js';
	import { trailForScope } from '#lib/nav.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { computeRoot } from '#lib/state/modules.svelte.js';
	import ContainerWorkspace from '#lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({ kind: 'project', project: page.params.project! });
</script>

<ContainerWorkspace
	{scope}
	section="compute"
	trail={trailForScope(scope, computeRoot(), (p) => inventory.title(p))}
/>
