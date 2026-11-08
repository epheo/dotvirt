<script lang="ts">
	import { api, type VMEvent } from '#lib/api.js';
	import { resource } from '#lib/resource.svelte.js';
	import EventsTable from './EventsTable.svelte';
	import Card from './Card.svelte';
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
	const VIEWS = [
		{ id: 'events', label: 'Events' },
		{ id: 'performance', label: 'Performance' },
	] as const;
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
</script>

{#snippet views()}
	<div
		role="group"
		aria-label="Show"
		class="inline-flex overflow-hidden rounded-md border border-line"
	>
		{#each VIEWS as v, i (v.id)}
			<button
				type="button"
				aria-pressed={view === v.id}
				onclick={() => (view = v.id)}
				class="px-2.5 py-1 {i ? 'border-l border-line' : ''} {view === v.id
					? 'bg-select text-accent-ink'
					: 'bg-panel text-ink-soft hover:bg-inset'}">{v.label}</button
			>
		{/each}
	</div>
{/snippet}

<Card title={view === 'events' ? 'Events' : 'Performance'} actions={views}>
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
		<EventsTable
			events={evRes.data}
			loading={evRes.loading}
			error={evRes.error}
			onretry={evRes.refresh}
			showVM
			{onselect}
		/>
	{/if}
</Card>
