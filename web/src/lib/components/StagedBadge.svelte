<script lang="ts">
	import { Pencil, Trash2 } from 'lucide-svelte';
	import type { DraftItem } from '$lib/api';
	import StatusPill from './StatusPill.svelte';

	// A VM with an unproposed staged change (the current user's draft). Shown in
	// place of the sync badge. Hover = peek at the diff; click = open the modal.
	let { item, onopen }: { item: DraftItem; onopen?: () => void } = $props();

	const isDelete = $derived(item.kind === 'delete');
	const summary = $derived(
		isDelete
			? 'Staged for removal'
			: item.changes
					.map((c) =>
						c.action === 'change'
							? `${c.field}: ${c.from ?? '∅'} → ${c.to ?? '∅'}`
							: c.action === 'add'
								? `${c.field}: + ${c.to ?? ''}`
								: `${c.field}: − ${c.from ?? ''}`,
					)
					.join('\n') || 'Staged change',
	);

	function open(e: MouseEvent) {
		e.stopPropagation();
		onopen?.();
	}
</script>

<StatusPill tone={isDelete ? 'danger' : 'info'} label="Staged" title={summary} onclick={open}>
	{#snippet icon()}
		{#if isDelete}<Trash2 size={11} />{:else}<Pencil size={11} />{/if}
	{/snippet}
</StatusPill>
