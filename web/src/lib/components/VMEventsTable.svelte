<script lang="ts">
	import { untrack } from 'svelte';
	import { api, type VM, type VMEvent } from '$lib/api';
	import { duration } from '$lib/format';
	import StatusDot from './StatusDot.svelte';

	// The Monitor tab's Kubernetes-events lane. Owns its load: it is mounted
	// only while the lane is visible, so the mount-time fetch IS the lazy load.
	let { vm }: { vm: VM } = $props();

	let events = $state<VMEvent[] | null>(null);
	let loading = $state(false);

	function load() {
		loading = true;
		api
			.events(vm.namespace, vm.name)
			.then((e) => (events = e))
			.catch(() => (events = [])) // a 401 signs out centrally via the api layer
			.finally(() => (loading = false));
	}

	// Reload on selection change only — the stream hands down a fresh vm object
	// every frame, and load() reads vm.namespace/name synchronously.
	const vmKey = $derived(`${vm.namespace}/${vm.name}`);
	$effect(() => {
		vmKey;
		untrack(() => {
			events = null;
			load();
		});
	});
</script>

{#if loading && !events}
	<div class="py-8 text-center text-sm text-ink-faint">Loading events…</div>
{:else if !events || events.length === 0}
	<div class="py-8 text-center text-sm text-ink-faint">No recent events.</div>
{:else}
	<table class="w-full text-[13px]">
		<thead class="text-left text-xs tracking-wide text-ink-faint uppercase">
			<tr class="border-b border-line">
				<th class="py-1.5 pr-3 font-medium">Type</th>
				<th class="py-1.5 pr-3 font-medium">Reason</th>
				<th class="py-1.5 pr-3 font-medium">Message</th>
				<th class="py-1.5 pr-3 font-medium">Object</th>
				<th class="py-1.5 font-medium">Last seen</th>
			</tr>
		</thead>
		<tbody class="divide-y divide-line-soft">
			{#each events as e, i (i)}
				<tr class={e.type === 'Warning' ? 'bg-warn-soft/40' : ''}>
					<td class="py-1.5 pr-3">
						<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
							<StatusDot tone={e.type === 'Warning' ? 'warn' : 'neutral'} size="xs" />
							{e.type}
						</span>
					</td>
					<td class="py-1.5 pr-3 font-medium text-ink-soft">{e.reason}</td>
					<td class="py-1.5 pr-3 text-ink-soft">{e.message}</td>
					<td class="py-1.5 pr-3 whitespace-nowrap text-ink-muted">
						{e.object === 'VirtualMachineInstance' ? 'VMI' : 'VM'}
					</td>
					<td class="py-1.5 whitespace-nowrap text-ink-muted">
						{duration(e.lastSeen)}{#if (e.count ?? 0) > 1}<span class="text-ink-faint">
								×{e.count}</span
							>{/if}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
{/if}
