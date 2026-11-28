<script lang="ts">
	import { desktops } from '#lib/state/desktops.svelte.js';

	// Where a desktop's or a pool's settings change: its project's repo, opened
	// in the forge when the console knows its page.
	let { project }: { project: string } = $props();

	const page = $derived(desktops.projects.find((p) => p.name === project)?.repoPage);
	const label = $derived(`${desktops.title(project)}'s repo`);
</script>

{#if page}<a class="underline" href={page} target="_blank" rel="noreferrer">
		{label}
	</a>{:else}{label}{/if}
