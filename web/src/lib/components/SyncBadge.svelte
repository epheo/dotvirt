<script lang="ts">
	import type { SyncStatus } from '$lib/api';
	import { TONE_DOT, type Tone } from '$lib/status';
	import Modal from './Modal.svelte';
	import StatusDot from './StatusDot.svelte';
	import StatusPill from './StatusPill.svelte';

	let {
		sync,
		error = '',
		compact = false,
	}: { sync: SyncStatus; error?: string; compact?: boolean } = $props();

	// vCenter-ish: green = in sync, red = drift, gray = not managed.
	const view = $derived(
		(
			{
				Synced: { tone: 'ok', label: 'Synced' },
				OutOfSync: { tone: 'danger', label: 'Out of sync' },
				NotTracked: { tone: 'neutral', label: 'Not tracked' },
				Unknown: { tone: 'neutral', label: 'Unknown' },
			} satisfies Record<SyncStatus, { tone: Tone; label: string }>
		)[sync],
	);

	// An out-of-sync VM has something to explain (an apply error, or just pending
	// drift) — clicking the badge/dot pops up the detail. Other states are inert.
	const clickable = $derived(sync === 'OutOfSync');
	let open = $state(false);

	function show(e: MouseEvent) {
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
			class="inline-block h-1.5 w-1.5 rounded-full {TONE_DOT.danger} cursor-pointer align-middle"
		></button>
	{/if}
{:else}
	<StatusPill
		tone={view.tone}
		label={error && clickable ? 'Sync failed' : view.label}
		title={error || 'ArgoCD sync status'}
		onclick={clickable ? show : undefined}
	/>
{/if}

{#if open}
	<Modal title="Sync detail — {view.label}" size="lg" onclose={() => (open = false)}>
		{#snippet icon()}<StatusDot tone={view.tone} />{/snippet}
		<div class="px-4 py-3 text-sm">
			{#if error}
				<p class="mb-2 text-ink-soft">ArgoCD could not apply this object:</p>
				<pre
					class="max-h-72 overflow-auto rounded bg-danger-soft/60 p-3 text-xs whitespace-pre-wrap text-danger-ink">{error}</pre>
			{:else}
				<p class="text-ink-soft">
					This object differs from git and ArgoCD hasn't applied the latest change yet. No apply
					error was reported — it's likely mid-sync or awaiting the next reconcile.
				</p>
			{/if}
		</div>
	</Modal>
{/if}
