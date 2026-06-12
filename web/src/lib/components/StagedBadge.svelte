<script lang="ts">
	import { Pencil, Trash2 } from 'lucide-svelte';
	import type { DraftItem } from '$lib/api';

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
								: `${c.field}: − ${c.from ?? ''}`
					)
					.join('\n') || 'Staged change'
	);
</script>

<button
	onclick={(e) => {
		e.stopPropagation();
		onopen?.();
	}}
	title={summary}
	class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium {isDelete
		? 'bg-red-100 text-red-700 hover:bg-red-200'
		: 'bg-blue-100 text-blue-700 hover:bg-blue-200'}"
>
	{#if isDelete}<Trash2 size={11} />{:else}<Pencil size={11} />{/if}
	Staged
</button>
