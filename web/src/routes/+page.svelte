<script lang="ts">
	import { api, streamInventory, type Inventory, type VM } from '$lib/api';
	import InventoryTree from '$lib/components/InventoryTree.svelte';
	import NewVMWizard from '$lib/components/NewVMWizard.svelte';
	import VMDetail from '$lib/components/VMDetail.svelte';

	let branches = $state<string[]>([]);
	let branch = $state<string>('');
	let inventory = $state<Inventory | null>(null);
	let selected = $state<VM | null>(null);
	let error = $state<string>('');
	let connected = $state(false);

	async function loadBranches() {
		try {
			branches = await api.branches();
			if (branches.length && !branch) branch = branches[0];
		} catch (e) {
			error = String(e);
		}
	}

	// Apply a pushed inventory, preserving the current selection if it still exists.
	function applyInventory(inv: Inventory) {
		inventory = inv;
		error = '';
		if (selected) {
			const still = inv.projects
				.flatMap((p) => p.vms)
				.find((v) => v.namespace === selected!.namespace && v.name === selected!.name);
			selected = still ?? null;
		}
	}

	$effect(() => {
		loadBranches();
	});

	// Live subscription: (re)subscribe whenever the branch changes. The WebSocket
	// pushes the current inventory on connect and on every cluster/argo/git change,
	// so there's no manual refresh. Switching branch tears down and reopens.
	$effect(() => {
		if (!branch) return;
		inventory = null; // clear while the new branch's first frame arrives
		const stop = streamInventory(branch, applyInventory, (c) => (connected = c));
		return stop;
	});

	const vmCount = $derived(
		inventory ? inventory.projects.reduce((n, p) => n + p.vms.length, 0) : 0
	);

	let showWizard = $state(false);
	const namespaces = $derived(inventory ? inventory.projects.map((p) => p.namespace) : []);
</script>

<div class="flex h-screen flex-col">
	<!-- Top bar -->
	<header class="flex items-center gap-3 border-b border-slate-300 bg-slate-800 px-4 py-2 text-white">
		<span class="font-semibold">dotvirt</span>
		<span class="text-slate-400">|</span>
		<label class="flex items-center gap-2 text-sm">
			<span class="text-slate-300">Branch</span>
			<select
				bind:value={branch}
				class="rounded border border-slate-600 bg-slate-700 px-2 py-1 text-sm text-white"
			>
				{#each branches as b (b)}
					<option value={b}>{b}</option>
				{/each}
			</select>
		</label>
		<span class="flex items-center gap-1.5 text-xs" title={connected ? 'Live — pushing updates' : 'Reconnecting…'}>
			<span
				class="inline-block h-2 w-2 rounded-full {connected
					? 'bg-green-400'
					: 'animate-pulse bg-amber-400'}"
			></span>
			<span class="text-slate-300">{connected ? 'Live' : 'Reconnecting…'}</span>
		</span>
		<button
			onclick={() => (showWizard = true)}
			disabled={!inventory}
			class="ml-auto rounded bg-blue-600 px-3 py-1 text-xs font-medium text-white hover:bg-blue-500 disabled:opacity-40"
		>
			+ New VM
		</button>
		<div class="text-xs text-slate-400">{vmCount} VMs</div>
	</header>

	{#if error}
		<div class="flex items-start gap-2 border-b border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700">
			<span class="font-medium">Error:</span>
			<span class="font-mono text-xs break-all">{error}</span>
		</div>
	{/if}

	<!-- Body: inventory tree | detail -->
	<div class="flex min-h-0 flex-1">
		<aside class="w-72 overflow-y-auto border-r border-slate-300 bg-white">
			{#if !inventory}
				<div class="space-y-2 p-3">
					{#each Array(5) as _, i (i)}
						<div class="h-5 animate-pulse rounded bg-slate-100"></div>
					{/each}
				</div>
			{:else if inventory && vmCount === 0}
				<div class="p-4 text-center text-xs text-slate-400">
					No VirtualMachines on <code class="text-slate-500">{branch}</code>.
				</div>
			{:else if inventory}
				<InventoryTree {inventory} {selected} onselect={(vm) => (selected = vm)} />
			{/if}
		</aside>
		<main class="min-w-0 flex-1 overflow-y-auto bg-white">
			<VMDetail
				vm={selected}
				{branch}
				onsaved={() => {
					// A new feature branch may now exist — refresh the switcher.
					loadBranches();
				}}
			/>
		</main>
	</div>

	{#if showWizard}
		<NewVMWizard
			{branch}
			{namespaces}
			onclose={() => (showWizard = false)}
			oncreated={() => loadBranches()}
		/>
	{/if}
</div>
