<script lang="ts">
	import type { VMEvent } from '$lib/api';
	import { duration } from '$lib/format';
	import StatusDot from './StatusDot.svelte';

	// The one events table behind every Monitor -> Events lane. Owns the
	// presentation filters (warnings, reason facets, time window); the caller
	// owns the fetch, so VM-level and container-level lanes stay one component.
	let {
		events,
		loading = false,
		showVM = false,
		onselect,
	}: {
		events: VMEvent[] | null;
		loading?: boolean;
		// Container lanes name the VM per row; the VM lane shows the object kind.
		showVM?: boolean;
		onselect?: (namespace: string, name: string) => void;
	} = $props();

	let warningsOnly = $state(false);
	let reason = $state(''); // '' = all
	let windowH = $state(0); // hours; 0 = all time

	const inWindow = (e: VMEvent) => {
		if (!windowH) return true;
		if (!e.lastSeen) return false;
		return Date.now() - new Date(e.lastSeen).getTime() <= windowH * 3600_000;
	};

	// Facets count within the time window (not the reason filter itself), so a
	// selected facet never empties its own chip.
	const facetBase = $derived(
		(events ?? []).filter((e) => inWindow(e) && (!warningsOnly || e.type === 'Warning')),
	);
	const reasonFacets = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const e of facetBase) counts.set(e.reason, (counts.get(e.reason) ?? 0) + 1);
		return [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 6);
	});
	// A selected reason that scrolled out of the facet list (or window) resets.
	$effect(() => {
		if (reason && !reasonFacets.some(([r]) => r === reason)) reason = '';
	});

	const shown = $derived(facetBase.filter((e) => !reason || e.reason === reason));
	const warnings = $derived((events ?? []).filter((e) => e.type === 'Warning').length);
</script>

{#if loading && !events}
	<div class="py-8 text-center text-sm text-ink-faint">Loading events…</div>
{:else if !events || events.length === 0}
	<div class="py-8 text-center text-sm text-ink-faint">No recent events.</div>
{:else}
	<div class="mb-2 flex flex-wrap items-center gap-1.5 text-xs">
		<button
			onclick={() => (warningsOnly = !warningsOnly)}
			class="rounded px-2 py-0.5 {warningsOnly
				? 'bg-warn-soft font-medium text-warn-ink'
				: 'bg-inset-strong text-ink-soft hover:bg-select-soft'}"
			title="Show warnings only"
		>
			Warnings {warnings}
		</button>
		<span class="mx-1 h-4 w-px bg-line-strong"></span>
		{#each reasonFacets as [r, n] (r)}
			<button
				onclick={() => (reason = reason === r ? '' : r)}
				class="rounded px-2 py-0.5 {reason === r
					? 'bg-select font-medium text-accent-ink'
					: 'bg-inset-strong text-ink-soft hover:bg-select-soft'}"
			>
				{r} <span class="text-ink-faint">{n}</span>
			</button>
		{/each}
		<select
			bind:value={windowH}
			class="ml-auto rounded border border-line-strong bg-panel px-1.5 py-0.5 text-xs text-ink-soft"
			aria-label="Time window"
		>
			<option value={0}>All time</option>
			<option value={1}>Last hour</option>
			<option value={6}>Last 6h</option>
			<option value={24}>Last 24h</option>
		</select>
	</div>

	{#if shown.length === 0}
		<div class="py-8 text-center text-sm text-ink-faint">No events match the filters.</div>
	{:else}
		<table class="w-full text-[13px]">
			<thead class="text-left text-xs tracking-wide text-ink-faint uppercase">
				<tr class="border-b border-line">
					<th class="py-1.5 pr-3 font-medium">Type</th>
					{#if showVM}<th class="py-1.5 pr-3 font-medium">VM</th>{/if}
					<th class="py-1.5 pr-3 font-medium">Reason</th>
					<th class="py-1.5 pr-3 font-medium">Message</th>
					{#if !showVM}<th class="py-1.5 pr-3 font-medium">Object</th>{/if}
					<th class="py-1.5 font-medium">Last seen</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-line-soft">
				{#each shown as e, i (i)}
					<tr class={e.type === 'Warning' ? 'bg-warn-soft/40' : ''}>
						<td class="py-1.5 pr-3">
							<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
								<StatusDot tone={e.type === 'Warning' ? 'warn' : 'neutral'} size="xs" />
								{e.type}
							</span>
						</td>
						{#if showVM}
							<td class="py-1.5 pr-3 whitespace-nowrap">
								{#if e.name}
									<button
										onclick={() => e.namespace && e.name && onselect?.(e.namespace, e.name)}
										class="font-medium text-ink-soft hover:text-accent-ink hover:underline"
										>{e.name}</button
									>
								{:else}—{/if}
							</td>
						{/if}
						<td class="py-1.5 pr-3 font-medium text-ink-soft">{e.reason}</td>
						<td class="py-1.5 pr-3 text-ink-soft">{e.message}</td>
						{#if !showVM}
							<td class="py-1.5 pr-3 whitespace-nowrap text-ink-muted">
								{e.object === 'VirtualMachineInstance' ? 'VMI' : 'VM'}
							</td>
						{/if}
						<td class="py-1.5 whitespace-nowrap text-ink-muted">
							{duration(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-ink-faint">
									×{e.count}</span
								>{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
		{#if shown.length < events.length}
			<p class="mt-1.5 text-right text-[11px] text-ink-faint">
				{shown.length} of {events.length} events shown
			</p>
		{/if}
	{/if}
{/if}
