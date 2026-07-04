<script lang="ts">
	import { untrack } from 'svelte';
	import { api, Unauthorized, type VM, type VMUsage } from '$lib/api';
	import { relativeAge } from '$lib/format';
	import { pollWhileVisible } from '$lib/poll';
	import UsageBar from './UsageBar.svelte';

	let { vm }: { vm: VM } = $props();

	let usage = $state<VMUsage | null>(null);
	let loading = $state(false);
	let failed = $state(false);

	async function load() {
		loading = true;
		try {
			usage = await api.vmUsage(vm.namespace, vm.name);
			failed = false;
		} catch (e) {
			if (e instanceof Unauthorized) return;
			failed = true;
		} finally {
			loading = false;
		}
	}

	// Reload on selection change only. Key on the VM identity, not the vm object:
	// the live stream hands down a fresh vm every frame, and load() reads
	// vm.namespace/name synchronously — untrack keeps those reads from re-firing
	// this effect each frame.
	const vmKey = $derived(`${vm.namespace}/${vm.name}`);
	$effect(() => {
		vmKey;
		untrack(load);
	});
	// Point-in-time, refreshed on a cadence (vCenter's Summary is a live snapshot),
	// paused while the tab is backgrounded.
	$effect(() => pollWhileVisible(load, 30000));
</script>

<section class="rounded border border-slate-200">
	<h3
		class="flex items-center justify-between border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
	>
		<span>Capacity &amp; usage</span>
		{#if usage}<span class="font-normal text-slate-400 normal-case"
				>updated {relativeAge(usage.updated)}</span
			>{/if}
	</h3>
	<div class="space-y-3 p-3">
		{#if usage}
			<UsageBar
				label="CPU"
				used={usage.cpu.used}
				total={100}
				unit="pct"
				color="var(--chart-1)"
				spark={usage.cpu.spark ?? []}
			/>
			<UsageBar
				label="Memory"
				used={usage.memory.used}
				total={usage.memory.total ?? 0}
				unit="bytes"
				color="var(--chart-2)"
				spark={usage.memory.spark ?? []}
			/>
			<UsageBar
				label="Storage (guest)"
				used={usage.storage.used}
				total={usage.storage.total ?? 0}
				unit="bytes"
				color="var(--chart-5)"
				spark={usage.storage.spark ?? []}
			/>
		{:else if loading}
			<p class="text-xs text-slate-400">Loading usage…</p>
		{:else if failed}
			<p class="text-xs text-slate-400">Usage metrics unavailable.</p>
		{/if}
	</div>
</section>
