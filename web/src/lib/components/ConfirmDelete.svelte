<script lang="ts">
	import type { Snippet } from 'svelte';
	import Modal from './Modal.svelte';

	// Shared type-to-confirm delete dialog, used for both single-VM (type the VM
	// name) and bulk (type "delete") removals. The body is passed as children.
	let {
		title,
		confirmWord,
		busy = false,
		error = '',
		onconfirm,
		onclose,
		children,
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

<Modal {title} danger {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm text-ink-soft">
		{@render children?.()}
		<label for="confirm-delete-input" class="mt-3 mb-1 block text-xs text-ink-muted">
			Type <span class="font-mono">{confirmWord}</span> to confirm:
		</label>
		<input
			id="confirm-delete-input"
			data-autofocus
			bind:value={text}
			class="w-full rounded border border-line-strong px-2 py-1 font-mono text-sm focus:border-red-400"
			placeholder={confirmWord}
		/>
		{#if error}
			<p class="mt-2 text-xs text-red-600">{error}</p>
		{/if}
	</div>
	{#snippet footer()}
		<button
			onclick={onclose}
			class="ml-auto rounded border border-line-strong px-3 py-1 text-sm text-ink-soft hover:bg-inset"
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
	{/snippet}
</Modal>
