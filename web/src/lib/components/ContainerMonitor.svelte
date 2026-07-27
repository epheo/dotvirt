<script lang="ts">
	import { api, type VMEvent } from '$lib/api';
	import { resource } from '$lib/resource.svelte';
	import EventsTable from './EventsTable.svelte';
	import MetricsPanel from './MetricsPanel.svelte';
	import TabBar from './TabBar.svelte';

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

	// Keyed on the namespace SET, not the array identity: the parent re-derives
	// the namespaces array every inventory frame, but its CONTENT only changes
	// on a real scope change - without this the slow /api/events call re-fires
	// continuously.
	const key = $derived([...namespaces].sort().join(','));
	const evRes = resource<VMEvent[]>(
		() => key,
		async () => {
			const all = await api.allEvents();
			const set = new Set(namespaces);
			return all.filter((e) => !e.namespace || set.has(e.namespace));
		},
		{ reset: true },
	);
	const events = $derived(evRes.failed ? [] : evRes.data);
	const loading = $derived(evRes.loading);
</script>

<div class="p-4">
	<TabBar
		class="mb-3 border-b border-line"
		tabs={[
			{ id: 'events', label: 'Events' },
			{ id: 'performance', label: 'Performance' },
		]}
		active={view}
		onchange={(v) => (view = v as typeof view)}
	/>
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
