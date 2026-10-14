<script lang="ts">
	import { page } from '$app/state';
	import type { Scope } from '#lib/lenses.js';
	import { trailForScope } from '#lib/nav.js';
	import ContainerWorkspace from '#lib/workspace/ContainerWorkspace.svelte';

	const scope = $derived<Scope>({ kind: 'storage', storageClass: page.params.class! });
</script>

<ContainerWorkspace {scope} section="storage" trail={trailForScope(scope)} />
