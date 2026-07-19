<script lang="ts">
	import { api, Unauthorized, type VMEvent } from '$lib/api';
	import EventsTable from './EventsTable.svelte';
	import MetricsPanel from './MetricsPanel.svelte';

	let {
		namespaces,
		scope = {},
		onselect,
	}: {
		namespaces: string[];
		// The backend-resolvable container scope, for the Performance charts.
		scope?: { project?: string; namespace?: string; node?: string };
		onselect?: (namespace: string, name: string) => void;
	} = $props();

	// Monitor sub-rail: events + performance, mirroring the VM detail's Monitor.
	let view = $state<'events' | 'performance'>('events');

	let events = $state<VMEvent[] | null>(null);
	let loading = $state(false);

	async function load() {
		loading = true;
		try {
			const all = await api.allEvents();
			const set = new Set(namespaces);
			events = all.filter((e) => !e.namespace || set.has(e.namespace));
		} catch (e) {
			if (e instanceof Unauthorized) return; // signed out centrally by the api layer
			events = [];
		} finally {
			loading = false;
		}
	}
	// Depend on a stable key, not the array identity: the parent re-derives the
	// namespaces array every inventory frame, but its CONTENT only changes on a real
	// scope change — without this the slow /api/events call re-fires continuously.
	const key = $derived([...namespaces].sort().join(','));
	$effect(() => {
		key;
		load();
	});
</script>

<div class="p-4">
	<div class="mb-3 flex gap-1 border-b border-line text-sm">
		{#each ['events', 'performance'] as const as v (v)}
			<button
				class="border-b-2 px-3 py-1 capitalize {view === v
					? 'border-accent text-accent-ink'
					: 'border-transparent text-ink-muted hover:text-ink-soft'}"
				onclick={() => (view = v)}
			>
				{v}
			</button>
		{/each}
	</div>
	{#if view === 'performance'}
		{#key `${scope.project ?? ''}|${scope.namespace ?? ''}|${scope.node ?? ''}`}
			<MetricsPanel
				load={(r) =>
					api.scopeMetrics(
						{ project: scope.project, namespace: scope.namespace, node: scope.node },
						r,
					)}
				emptyText="No VM metrics in this scope yet."
			/>
		{/key}
	{:else}
		<EventsTable {events} {loading} showVM {onselect} />
	{/if}
</div>
