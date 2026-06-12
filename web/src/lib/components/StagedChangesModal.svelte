<script lang="ts">
	import { X } from 'lucide-svelte';
	import type { DraftItem } from '$lib/api';
	import ChangeList from './ChangeList.svelte';

	// Per-VM view of a staged change: the diff + discard / open-in-Changes.
	let {
		item,
		busy = false,
		onclose,
		ondiscard,
		onreview
	}: {
		item: DraftItem;
		busy?: boolean;
		onclose: () => void;
		ondiscard: () => void;
		onreview: () => void;
	} = $props();

	const isDelete = $derived(item.kind === 'delete');
</script>

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && onclose()}
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	role="presentation"
>
	<div class="flex max-h-[80vh] w-full max-w-md flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2 class="text-base font-semibold text-slate-800">Staged changes — {item.name}</h2>
			<button onclick={onclose} class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
		</header>
		<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm">
			<p class="mb-2 text-xs text-slate-500">{item.namespace}/{item.name} · not yet proposed</p>
			{#if isDelete}
				<p class="text-slate-700">
					This VM is <strong>staged for removal</strong> — it'll be deleted from the cluster when the
					pull request merges.
				</p>
			{:else}
				<ChangeList changes={item.changes} />
			{/if}
		</div>
		<footer class="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
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
		</footer>
	</div>
</div>
