<script lang="ts">
	import type { DraftItem } from '$lib/api';
	import ChangeList from './ChangeList.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import Modal from './Modal.svelte';

	// Per-VM view of a staged change: the diff + discard / open-in-Changes.
	let {
		item,
		busy = false,
		onclose,
		ondiscard,
		onreview,
	}: {
		item: DraftItem;
		busy?: boolean;
		onclose: () => void;
		ondiscard: () => void;
		onreview: () => void;
	} = $props();

	const isDelete = $derived(item.kind === 'delete');
</script>

<Modal title="Staged changes — {item.name}" {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm">
		<div class="mb-2 flex items-center justify-between gap-3">
			<p class="text-xs text-slate-500">{item.namespace}/{item.name} · not yet proposed</p>
			<GitOpsStepper stage="staged" />
		</div>
		{#if isDelete}
			<p class="text-slate-700">
				This VM is <strong>staged for removal</strong> — it'll be deleted from the cluster when the pull
				request merges.
			</p>
		{:else}
			<ChangeList changes={item.changes} />
		{/if}
	</div>
	{#snippet footer()}
		<button
			onclick={ondiscard}
			disabled={busy}
			class="rounded border border-slate-300 px-3 py-1 text-sm text-slate-700 hover:bg-slate-50 disabled:opacity-50"
		>
			Discard
		</button>
		<button
			onclick={onreview}
			class="ml-auto rounded bg-blue-600 px-3 py-1 text-sm font-medium text-white hover:bg-blue-500"
		>
			Review &amp; propose →
		</button>
	{/snippet}
</Modal>
