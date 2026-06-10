<script lang="ts">
	import type { VM } from '$lib/api';
	import Console from './Console.svelte';
	import EditSettings from './EditSettings.svelte';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let { vm, branch, onsaved }: { vm: VM | null; branch: string; onsaved?: () => void } = $props();

	type Tab = 'summary' | 'console';
	let tab = $state<Tab>('summary');
	let editing = $state(false);

	// The `running` branch mirrors the cluster and is dotvirt-owned: not editable.
	const editable = $derived(branch !== 'running' && branch !== '');

	$effect(() => {
		// Reset to summary when the selection changes.
		void vm;
		tab = 'summary';
		editing = false;
	});
</script>

{#if vm}
	<div class="flex h-full flex-col">
		<div class="border-b border-slate-200 px-4 pt-4">
			<div class="mb-3 flex items-center gap-2">
				<PowerDot power={vm.power} />
				<h2 class="text-lg font-semibold text-slate-800">{vm.name}</h2>
				<span class="rounded bg-slate-200 px-1.5 py-0.5 text-xs text-slate-600">{vm.namespace}</span>
				<SyncBadge sync={vm.sync} />
				<button
					onclick={() => (editing = true)}
					disabled={!editable}
					title={editable ? 'Edit settings' : 'Switch off the running branch to edit'}
					class="ml-auto rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-40"
				>
					Edit Settings
				</button>
			</div>
			<nav class="flex gap-1 text-sm">
				{#each ['summary', 'console'] as const as t (t)}
					<button
						class="border-b-2 px-3 py-1.5 capitalize {tab === t
							? 'border-blue-600 text-blue-700'
							: 'border-transparent text-slate-500 hover:text-slate-700'}"
						onclick={() => (tab = t)}
					>
						{t}
					</button>
				{/each}
			</nav>
		</div>

		<div class="min-h-0 flex-1 overflow-y-auto p-4">
			{#if tab === 'summary'}
				<table class="w-full text-[13px]">
					<tbody class="divide-y divide-slate-100">
						<tr>
							<td class="w-40 py-1.5 align-top text-slate-500">Power (desired)</td>
							<td class="py-1.5 text-slate-800">{vm.power}</td>
						</tr>
						{#if vm.phase}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Status (actual)</td>
								<td class="py-1.5 text-slate-800">{vm.phase}</td>
							</tr>
						{/if}
						{#if vm.guestIP}
							<tr>
								<td class="py-1.5 align-top text-slate-500">IP address</td>
								<td class="py-1.5 font-mono text-slate-800">{vm.guestIP}</td>
							</tr>
						{/if}
						{#if vm.nodeName}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Node</td>
								<td class="py-1.5 text-slate-800">{vm.nodeName}</td>
							</tr>
						{/if}
						{#if vm.instancetype}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Instance type</td>
								<td class="py-1.5 text-slate-800">{vm.instancetype}</td>
							</tr>
						{/if}
						{#if vm.preference}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Preference</td>
								<td class="py-1.5 text-slate-800">{vm.preference}</td>
							</tr>
						{/if}
						<tr>
							<td class="py-1.5 align-top text-slate-500">CPU</td>
							<td class="py-1.5 text-slate-800">{vm.cpuCores ?? '—'} vCPU</td>
						</tr>
						<tr>
							<td class="py-1.5 align-top text-slate-500">Memory</td>
							<td class="py-1.5 text-slate-800">{vm.memory ?? '—'}</td>
						</tr>
						{#if vm.disks?.length}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Disks</td>
								<td class="py-1.5 text-slate-800">
									{#each vm.disks as d (d.name)}
										<div>{d.name} <span class="text-slate-400">({d.type}{d.size ? ` · ${d.size}` : ''})</span></div>
									{/each}
								</td>
							</tr>
						{/if}
						{#if vm.networks?.length}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Networks</td>
								<td class="py-1.5 text-slate-800">
									{#each vm.networks as n (n.name)}
										<div>{n.name} <span class="text-slate-400">({n.network})</span></div>
									{/each}
								</td>
							</tr>
						{/if}
						{#if vm.labels && Object.keys(vm.labels).length}
							<tr>
								<td class="py-1.5 align-top text-slate-500">Labels</td>
								<td class="py-1.5">
									{#each Object.entries(vm.labels) as [k, v] (k)}
										<span class="mr-1 mb-1 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-600">{k}={v}</span>
									{/each}
								</td>
							</tr>
						{/if}
						<tr>
							<td class="py-1.5 align-top text-slate-500">Source</td>
							<td class="py-1.5 font-mono text-xs text-slate-600">{vm.sourceFile}</td>
						</tr>
					</tbody>
				</table>
			{:else}
				{#key `${vm.namespace}/${vm.name}`}
					<Console {vm} />
				{/key}
			{/if}
		</div>
	</div>

	{#if editing}
		<EditSettings
			{vm}
			{branch}
			onclose={() => (editing = false)}
			onsaved={() => onsaved?.()}
		/>
	{/if}
{:else}
	<div class="flex h-full items-center justify-center text-sm text-slate-400">
		Select a VM from the inventory
	</div>
{/if}
