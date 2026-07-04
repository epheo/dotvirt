<script lang="ts">
	import type { SyncStatus } from '$lib/api';
	import Modal from './Modal.svelte';
	let {
		sync,
		error = '',
		compact = false
	}: { sync: SyncStatus; error?: string; compact?: boolean } = $props();

	// vCenter-ish: green = in sync, amber/red = drift, gray = not managed.
	const style = $derived(
		{
			Synced: { bg: 'bg-green-100', fg: 'text-green-700', label: 'Synced', dot: 'bg-green-500' },
			OutOfSync: { bg: 'bg-red-100', fg: 'text-red-700', label: 'Out of sync', dot: 'bg-red-500' },
			NotTracked: {
				bg: 'bg-slate-100',
				fg: 'text-slate-500',
				label: 'Not tracked',
				dot: 'bg-slate-300'
			},
			Unknown: { bg: 'bg-slate-100', fg: 'text-slate-500', label: 'Unknown', dot: 'bg-slate-300' }
		}[sync]
	);

	// An OutOfSync VM has something to explain (an apply error, or just pending
	// drift) — clicking the badge/dot pops up the detail. Other states are inert.
	const clickable = $derived(sync === 'OutOfSync');
	let open = $state(false);

	function show(e: MouseEvent) {
		if (!clickable) return;
		e.stopPropagation(); // don't also trigger row selection in the inventory tree
		open = true;
	}
</script>

{#if compact}
	{#if sync === 'OutOfSync'}
		<button
			type="button"
			onclick={show}
			aria-label="Show sync detail"
			title={error || 'ArgoCD: out of sync'}
			class="inline-block h-1.5 w-1.5 rounded-full {style.dot} cursor-pointer align-middle"
		></button>
	{/if}
{:else if clickable}
	<button
		type="button"
		onclick={show}
		title={error || 'ArgoCD sync status'}
		class="inline-flex cursor-pointer items-center gap-1 rounded px-1.5 py-0.5 text-xs hover:brightness-95 {style.bg} {style.fg}"
	>
		<span class="inline-block h-1.5 w-1.5 rounded-full {style.dot}"></span>
		{error ? 'Sync failed' : style.label}
	</button>
{:else}
	<span
		class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs {style.bg} {style.fg}"
		title="ArgoCD sync status"
	>
		<span class="inline-block h-1.5 w-1.5 rounded-full {style.dot}"></span>
		{style.label}
	</span>
{/if}

{#if open}
	<Modal title="Sync detail — {style.label}" size="lg" onclose={() => (open = false)}>
		{#snippet icon()}<span class="inline-block h-2 w-2 rounded-full {style.dot}"></span>{/snippet}
		<div class="px-4 py-3 text-sm">
			{#if error}
				<p class="mb-2 text-slate-600">ArgoCD could not apply this object:</p>
				<pre
					class="max-h-72 overflow-auto rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
			{:else}
				<p class="text-slate-600">
					This object differs from git and ArgoCD hasn't applied the latest change yet. No apply
					error was reported — it's likely mid-sync or awaiting the next reconcile.
				</p>
			{/if}
		</div>
	</Modal>
{/if}
