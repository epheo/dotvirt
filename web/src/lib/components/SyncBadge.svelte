<script lang="ts">
	import type { SyncStatus } from '$lib/api';
	let { sync, compact = false }: { sync: SyncStatus; compact?: boolean } = $props();

	// vCenter-ish: green = in sync, amber/red = drift, gray = not managed.
	const style = $derived(
		{
			Synced: { bg: 'bg-green-100', fg: 'text-green-700', label: 'Synced', dot: 'bg-green-500' },
			OutOfSync: { bg: 'bg-red-100', fg: 'text-red-700', label: 'OutOfSync', dot: 'bg-red-500' },
			NotTracked: { bg: 'bg-slate-100', fg: 'text-slate-500', label: 'Not tracked', dot: 'bg-slate-300' },
			Unknown: { bg: 'bg-slate-100', fg: 'text-slate-500', label: 'Unknown', dot: 'bg-slate-300' }
		}[sync]
	);
</script>

{#if compact}
	<!-- Tree row: a small dot, only drawn when notable (drift or unmanaged). -->
	{#if sync === 'OutOfSync'}
		<span class="inline-block h-1.5 w-1.5 rounded-full {style.dot}" title="ArgoCD: OutOfSync"
		></span>
	{/if}
{:else}
	<span
		class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs {style.bg} {style.fg}"
		title="ArgoCD sync status"
	>
		<span class="inline-block h-1.5 w-1.5 rounded-full {style.dot}"></span>
		{style.label}
	</span>
{/if}
