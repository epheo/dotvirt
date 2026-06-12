<script lang="ts">
	import { api, Unauthorized, type VMEvent } from '$lib/api';
	import { relativeAge } from '$lib/format';

	let {
		namespaces,
		onselect
	}: { namespaces: string[]; onselect?: (namespace: string, name: string) => void } = $props();

	let events = $state<VMEvent[] | null>(null);
	let loading = $state(false);

	async function load() {
		loading = true;
		try {
			const all = await api.allEvents();
			const set = new Set(namespaces);
			events = all.filter((e) => !e.namespace || set.has(e.namespace));
		} catch (e) {
			if (e instanceof Unauthorized) return;
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
	{#if loading && !events}
		<div class="py-8 text-center text-sm text-slate-400">Loading events…</div>
	{:else if !events || events.length === 0}
		<div class="py-8 text-center text-sm text-slate-400">No recent events in scope.</div>
	{:else}
		<table class="w-full text-[13px]">
			<thead class="text-left text-xs tracking-wide text-slate-400 uppercase">
				<tr class="border-b border-slate-200">
					<th class="py-1.5 pr-3 font-medium">Type</th>
					<th class="py-1.5 pr-3 font-medium">VM</th>
					<th class="py-1.5 pr-3 font-medium">Reason</th>
					<th class="py-1.5 pr-3 font-medium">Message</th>
					<th class="py-1.5 font-medium">Last seen</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100">
				{#each events as e, i (i)}
					<tr class={e.type === 'Warning' ? 'bg-amber-50/40' : ''}>
						<td class="py-1.5 pr-3">
							<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
								<span
									class="h-1.5 w-1.5 rounded-full {e.type === 'Warning'
										? 'bg-amber-500'
										: 'bg-slate-400'}"
								></span>
								{e.type}
							</span>
						</td>
						<td class="py-1.5 pr-3 whitespace-nowrap">
							{#if e.name}
								<button
									onclick={() => e.namespace && e.name && onselect?.(e.namespace, e.name)}
									class="font-medium text-slate-700 hover:text-blue-700 hover:underline">{e.name}</button
								>
							{:else}—{/if}
						</td>
						<td class="py-1.5 pr-3 font-medium text-slate-700">{e.reason}</td>
						<td class="py-1.5 pr-3 text-slate-600">{e.message}</td>
						<td class="py-1.5 whitespace-nowrap text-slate-500">
							{relativeAge(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-slate-400"> ×{e.count}</span
								>{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>
