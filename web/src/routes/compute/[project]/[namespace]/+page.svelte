<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '#lib/lenses.js';
	import { trailForScope } from '#lib/nav.js';
	import { computeRoot } from '#lib/state/modules.svelte.js';
	import ContainerWorkspace from '#lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({
		kind: 'namespace',
		project: page.params.project!,
		namespace: page.params.namespace!,
	});
</script>

<ContainerWorkspace {scope} section="compute" trail={trailForScope(scope, computeRoot())} />
