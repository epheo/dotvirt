<script lang="ts">
	import { api, type Change, type VM } from '$lib/api';
	import ChangeList from './ChangeList.svelte';
	import Console from './Console.svelte';
	import EditSettings from './EditSettings.svelte';
	import PowerDot from './PowerDot.svelte';
	import SyncBadge from './SyncBadge.svelte';

	let { vm, onstaged }: { vm: VM | null; onstaged?: () => void } = $props();

	type Tab = 'summary' | 'console';
	let tab = $state<Tab>('summary');
	let editing = $state(false);

	// Delete is destructive once the PR merges, so it's gated behind a confirm
	// dialog that requires typing the VM name.
	let deleting = $state(false);
	let confirmName = $state('');
	let deleteBusy = $state(false);
	let deleteErr = $state('');

	// Drift detail (running vs main) for the selected VM.
	let driftChanges = $state<Change[] | null>(null);
	let showDrift = $state(false);
	let reconciling = $state(false);
	let reconcileMsg = $state('');

	function loadDrift(ns: string, name: string) {
		api
			.drift(ns, name)
			.then((d) => (driftChanges = d.drift ? d.changes : []))
			.catch(() => (driftChanges = null));
	}

	$effect(() => {
		// Reset when the selection changes, and (re)load drift for this VM.
		const cur = vm;
		tab = 'summary';
		editing = false;
		deleting = false;
		confirmName = '';
		deleteErr = '';
		driftChanges = null;
		showDrift = false;
		reconcileMsg = '';
		if (cur) loadDrift(cur.namespace, cur.name);
	});

	async function adopt() {
		if (!vm) return;
		reconciling = true;
		reconcileMsg = '';
		try {
			await api.adopt(vm.namespace, vm.name);
			reconcileMsg = 'Live state staged into Changes — open a PR to adopt it into git.';
			onstaged?.();
		} catch (e) {
			reconcileMsg = String(e);
		} finally {
			reconciling = false;
		}
	}

	async function resync() {
		if (!vm) return;
		reconciling = true;
		reconcileMsg = '';
		try {
			const r = await api.resync(vm.namespace, vm.name);
			reconcileMsg = `Re-sync triggered on ArgoCD app "${r.application}".`;
		} catch (e) {
			reconcileMsg = String(e);
		} finally {
			reconciling = false;
		}
	}

	async function confirmDelete() {
		if (!vm || confirmName !== vm.name) return;
		deleteBusy = true;
		deleteErr = '';
		try {
			await api.stageDelete(vm.namespace, vm.name);
			deleting = false;
			confirmName = '';
			onstaged?.();
		} catch (e) {
			deleteErr = String(e);
		} finally {
			deleteBusy = false;
		}
	}
</script>

{#if vm}
	<div class="flex h-full flex-col">
		<div class="border-b border-slate-200 px-4 pt-4">
			<div class="mb-3 flex items-center gap-2">
				<PowerDot power={vm.power} />
				<h2 class="text-lg font-semibold text-slate-800">{vm.name}</h2>
				<span class="rounded bg-slate-200 px-1.5 py-0.5 text-xs text-slate-600">{vm.namespace}</span>
				<SyncBadge sync={vm.sync} />
				<div class="ml-auto flex items-center gap-2">
					<button
						onclick={() => (editing = true)}
						title="Edit settings"
						class="rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50"
					>
						Edit Settings
					</button>
					<button
						onclick={() => {
							deleting = true;
							confirmName = '';
							deleteErr = '';
						}}
						title="Delete this VM (stages a removal into Changes)"
						class="rounded border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50"
					>
						Delete VM
					</button>
				</div>
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

				{#if driftChanges && driftChanges.length > 0}
					<div class="mt-4 rounded border border-amber-200 bg-amber-50">
						<button
							onclick={() => (showDrift = !showDrift)}
							class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm font-medium text-amber-800"
						>
							<span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>
							Drift — cluster differs from git ({driftChanges.length})
							<span class="ml-auto text-xs text-amber-600">{showDrift ? '▾' : '▸'}</span>
						</button>
						{#if showDrift}
							<div class="border-t border-amber-200 px-3 py-2">
								<p class="mb-1 text-xs text-amber-700">Desired (main) → Actual (running):</p>
								<ChangeList changes={driftChanges} />
								<div class="mt-3 flex items-center gap-2">
									<button
										onclick={adopt}
										disabled={reconciling}
										title="Stage the live state into a PR so git matches the cluster"
										class="rounded border border-amber-400 bg-white px-2.5 py-1 text-xs font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50"
									>
										Adopt into PR (running→main)
									</button>
									<button
										onclick={resync}
										disabled={reconciling}
										title="Trigger ArgoCD to reconcile the cluster back to git"
										class="rounded border border-amber-400 bg-white px-2.5 py-1 text-xs font-medium text-amber-800 hover:bg-amber-100 disabled:opacity-50"
									>
										Re-sync from git (main→running)
									</button>
								</div>
								{#if reconcileMsg}
									<p class="mt-2 text-xs text-slate-600">{reconcileMsg}</p>
								{/if}
							</div>
						{/if}
					</div>
				{/if}
			{:else}
				{#key `${vm.namespace}/${vm.name}`}
					<Console {vm} />
				{/key}
			{/if}
		</div>
	</div>

	{#if editing}
		<EditSettings {vm} onclose={() => (editing = false)} onstaged={() => onstaged?.()} />
	{/if}

	{#if deleting}
		<div
			class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
			onclick={(e) => e.target === e.currentTarget && (deleting = false)}
			onkeydown={(e) => e.key === 'Escape' && (deleting = false)}
			role="presentation"
		>
			<div class="w-full max-w-md rounded-lg bg-white shadow-xl">
				<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
					<h2 class="text-base font-semibold text-red-700">Delete VM — {vm.name}</h2>
					<button onclick={() => (deleting = false)} class="text-slate-400 hover:text-slate-700">✕</button>
				</header>
				<div class="px-5 py-4 text-sm text-slate-700">
					<p class="mb-3">
						This removes <span class="font-mono text-xs">{vm.sourceFile}</span> from git and stages
						the change into <strong>Changes</strong>. The VM is deleted from the cluster only when the
						pull request is merged.
					</p>
					<label for="delete-confirm" class="mb-1 block text-xs text-slate-500">
						Type <span class="font-mono">{vm.name}</span> to confirm:
					</label>
					<input
						id="delete-confirm"
						bind:value={confirmName}
						class="w-full rounded border border-slate-300 px-2 py-1 font-mono text-sm focus:border-red-400 focus:outline-none"
						placeholder={vm.name}
					/>
					{#if deleteErr}
						<p class="mt-2 text-xs text-red-600">{deleteErr}</p>
					{/if}
				</div>
				<footer class="flex justify-end gap-2 border-t border-slate-200 px-5 py-3">
					<button
						onclick={() => (deleting = false)}
						class="rounded border border-slate-300 px-3 py-1 text-sm text-slate-700 hover:bg-slate-50"
					>
						Cancel
					</button>
					<button
						onclick={confirmDelete}
						disabled={confirmName !== vm.name || deleteBusy}
						class="rounded bg-red-600 px-3 py-1 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
					>
						Delete
					</button>
				</footer>
			</div>
		</div>
	{/if}
{:else}
	<div class="flex h-full items-center justify-center text-sm text-slate-400">
		Select a VM from the inventory
	</div>
{/if}
