<script lang="ts">
	import { untrack } from 'svelte';
	import type { NextStep } from '#lib/api.js';
	import { returnPath } from '#lib/signin.js';
	import { forge } from '#lib/state/forge.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Button from './Button.svelte';

	// The one click a refused write names as its way forward, beside the
	// server's sentence: connect the git account the write goes to the forge
	// as, back on this page; or open the project's repo in the forge, where
	// its owners grant write. class is for layout only.
	let {
		next,
		project,
		class: cls = '',
	}: { next: NextStep; project: string; class?: string } = $props();

	const repo = $derived.by(() => {
		const p = inventory.projects.find((x) => x.name === project);
		return p?.repoPage || p?.repo || '';
	});

	// The user menu reads the connection the refusal found missing.
	$effect(() => {
		if (next === 'forge-connect') untrack(() => void forge.load());
	});
</script>

{#if next === 'forge-connect'}
	<Button size="sm" class={cls} onclick={() => forge.connect(returnPath(new URL(location.href)))}
		>Connect git account</Button
	>
{:else if next === 'forge-write' && repo}
	<Button size="sm" variant="secondary" class={cls} href={repo} target="_blank" rel="noopener"
		>Open the repo in the forge</Button
	>
{/if}
