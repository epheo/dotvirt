<script lang="ts">
	import { api, Unauthorized, type VM, type VMUsage } from '$lib/api';
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

	$effect(() => {
		vm.namespace;
		vm.name;
		load();
	});
	// Point-in-time, refreshed on a cadence (vCenter's Summary is a live snapshot).
	$effect(() => {
		const id = setInterval(load, 30000);
		return () => clearInterval(id);
	});

	function ago(ts: number): string {
		const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
		return s < 60 ? `${s}s ago` : `${Math.floor(s / 60)}m ago`;
	}
</script>

<section class="rounded border border-slate-200">
	<h3
		class="flex items-center justify-between border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
	>
		<span>Capacity &amp; usage</span>
		{#if usage}<span class="font-normal text-slate-400 normal-case">updated {ago(usage.updated)}</span
			>{/if}
	</h3>
	<div class="space-y-3 p-3">
		{#if usage}
			<UsageBar label="CPU" used={usage.cpu.used} total={100} unit="pct" color="#2563eb" spark={usage.cpu.spark ?? []} />
			<UsageBar
				label="Memory"
				used={usage.memory.used}
				total={usage.memory.total ?? 0}
				unit="bytes"
				color="#0d9488"
				spark={usage.memory.spark ?? []}
			/>
			<UsageBar
				label="Storage (guest)"
				used={usage.storage.used}
				total={usage.storage.total ?? 0}
				unit="bytes"
				color="#7c3aed"
				spark={usage.storage.spark ?? []}
			/>
		{:else if loading}
			<p class="text-xs text-slate-400">Loading usage…</p>
		{:else if failed}
			<p class="text-xs text-slate-400">Usage metrics unavailable.</p>
		{/if}
	</div>
</section>
