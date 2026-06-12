<script lang="ts">
	import type { Snippet } from 'svelte';
	import { X } from 'lucide-svelte';

	// Shared type-to-confirm delete dialog, used for both single-VM (type the VM
	// name) and bulk (type "delete") removals. The body is passed as children.
	let {
		title,
		confirmWord,
		busy = false,
		error = '',
		onconfirm,
		onclose,
		children
	}: {
		title: string;
		confirmWord: string; // the exact text the user must type to enable Delete
		busy?: boolean;
		error?: string;
		onconfirm: () => void;
		onclose: () => void;
		children?: Snippet;
	} = $props();

	let text = $state('');
	const ready = $derived(text === confirmWord && !busy);
</script>

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && onclose()}
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	role="presentation"
>
	<div class="flex max-h-[80vh] w-full max-w-md flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2 class="text-base font-semibold text-red-700">{title}</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
		</header>
		<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm text-slate-700">
			{@render children?.()}
			<label for="confirm-delete-input" class="mt-3 mb-1 block text-xs text-slate-500">
				Type <span class="font-mono">{confirmWord}</span> to confirm:
			</label>
			<input
				id="confirm-delete-input"
				bind:value={text}
				class="w-full rounded border border-slate-300 px-2 py-1 font-mono text-sm focus:border-red-400 focus:outline-none"
				placeholder={confirmWord}
			/>
			{#if error}
				<p class="mt-2 text-xs text-red-600">{error}</p>
			{/if}
		</div>
		<footer class="flex justify-end gap-2 border-t border-slate-200 px-5 py-3">
			<button
				onclick={onclose}
				class="rounded border border-slate-300 px-3 py-1 text-sm text-slate-700 hover:bg-slate-50"
			>
				Cancel
			</button>
			<button
				onclick={onconfirm}
				disabled={!ready}
				class="rounded bg-red-600 px-3 py-1 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
			>
				Delete
			</button>
		</footer>
	</div>
</div>
