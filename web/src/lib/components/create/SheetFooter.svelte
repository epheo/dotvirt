<script lang="ts">
	import type { Snippet } from 'svelte';
	import Button from '../Button.svelte';

	// The sheet's verbs: what happens on merge on the left (a start box or a
	// note), then Cancel and the one primary. A disabled primary always says
	// what it is waiting for.
	let {
		label,
		busyLabel = 'Staging...',
		missing = [],
		busy = false,
		onsubmit,
		oncancel,
		left,
		note = 'Nothing reaches the cluster until the pull request merges.',
	}: {
		label: string;
		busyLabel?: string;
		missing?: string[];
		busy?: boolean;
		onsubmit: () => void;
		oncancel: () => void;
		left?: Snippet;
		note?: string;
	} = $props();
</script>

<div class="flex min-w-0 flex-[1_1_220px] items-center gap-2 text-ink-soft">
	{#if left}{@render left()}{:else}<span class="text-ink-muted">{note}</span>{/if}
</div>
{#if missing.length}
	<span class="text-xs text-warn-ink" role="status">{missing[0]}</span>
{/if}
<Button variant="secondary" class="h-9! px-3!" onclick={oncancel}>Cancel</Button>
<Button class="h-9! px-4!" onclick={onsubmit} disabled={busy || missing.length > 0}
	>{busy ? busyLabel : label}</Button
>
