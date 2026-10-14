<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '#lib/lenses.js';
	import { trailForScope } from '#lib/nav.js';
	import ContainerWorkspace from '#lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({ kind: 'node', node: page.params.node! });
</script>

<ContainerWorkspace {scope} section="hosts" trail={trailForScope(scope)} />
