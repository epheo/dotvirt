<script lang="ts">
	import type { DesktopClass } from '#lib/api.js';
	import RepoLink from './RepoLink.svelte';

	// A class a pool or a desktop names that does not exist: what it costs,
	// and the two ways out.
	let {
		name,
		outcome,
		project,
		classes,
	}: { name: string; outcome: string; project: string; classes: DesktopClass[] } = $props();
</script>

<div
	class="rounded-lg border border-danger/30 bg-danger-soft px-4 py-3 text-[13px] text-danger-ink"
>
	<p class="font-medium">Class {name} does not exist, so {outcome}.</p>
	<p>
		Point it at a class that exists, in <RepoLink {project} />, or ask a cluster admin to add {name}.
	</p>
	{#if classes.length}
		<p>Classes on the cluster: {classes.map((c) => c.name).join(', ')}.</p>
	{/if}
</div>
