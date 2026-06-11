<script lang="ts">
	import { Activity, ChevronDown, ChevronRight, Cpu, HardDrive, MemoryStick, Pencil, Trash2, X } from 'lucide-svelte';
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

	// Uptime since the VMI entered Running, formatted compactly (e.g. "3d 21h").
	function uptime(iso?: string): string {
		if (!iso) return '';
		const start = new Date(iso).getTime();
		if (Number.isNaN(start)) return '';
		let s = Math.max(0, Math.floor((Date.now() - start) / 1000));
		const d = Math.floor(s / 86400);
		const h = Math.floor((s % 86400) / 3600);
		const m = Math.floor((s % 3600) / 60);
		if (d > 0) return `${d}d ${h}h`;
		if (h > 0) return `${h}h ${m}m`;
		return `${m}m`;
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
						class="flex items-center gap-1.5 rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50"
					>
						<Pencil size={13} /> Edit Settings
					</button>
					<button
						onclick={() => {
							deleting = true;
							confirmName = '';
							deleteErr = '';
						}}
						title="Delete this VM (stages a removal into Changes)"
						class="flex items-center gap-1.5 rounded border border-red-300 px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50"
					>
						<Trash2 size={13} /> Delete VM
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
				{#snippet field(label: string, value: string)}
					<div class="flex justify-between gap-3 px-3 py-1.5">
						<dt class="shrink-0 text-slate-500">{label}</dt>
						<dd class="min-w-0 truncate text-right text-slate-800">{value || '—'}</dd>
					</div>
				{/snippet}

				<!-- At-a-glance tiles: the vCenter-style capacity summary. -->
				<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><Cpu size={13} /> CPU</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">
							{vm.cpuCores ?? '—'}<span class="ml-1 text-sm font-normal text-slate-500">vCPU</span>
						</div>
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><MemoryStick size={13} /> Memory</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{vm.memory ?? '—'}</div>
						{#if vm.memoryActual && vm.memoryActual !== vm.memory}
							<div class="text-xs text-slate-400">{vm.memoryActual} live</div>
						{/if}
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><HardDrive size={13} /> Disks</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{vm.disks?.length ?? 0}</div>
					</div>
					<div class="rounded border border-slate-200 bg-slate-50 p-3">
						<div class="flex items-center gap-1.5 text-xs text-slate-500"><Activity size={13} /> Status</div>
						<div class="mt-1 text-lg font-semibold text-slate-800">{vm.phase ?? vm.power}</div>
						{#if uptime(vm.startedAt)}<div class="text-xs text-slate-400">up {uptime(vm.startedAt)}</div>{/if}
					</div>
				</div>

				<div class="mt-4 grid gap-4 md:grid-cols-2">
					<!-- Guest & runtime: live identity reported by the guest agent. -->
					<section class="rounded border border-slate-200">
						<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">
							Guest &amp; runtime
						</h3>
						<dl class="divide-y divide-slate-100 text-[13px]">
							{@render field('Operating system', vm.os ?? '')}
							{@render field('Power (desired)', vm.power)}
							{@render field('Status (actual)', vm.phase ?? '')}
							<div class="flex justify-between gap-3 px-3 py-1.5">
								<dt class="shrink-0 text-slate-500">IP addresses</dt>
								<dd class="min-w-0 text-right font-mono text-xs text-slate-800">
									{#if vm.ips?.length}
										{#each vm.ips as ip (ip)}<div>{ip}</div>{/each}
									{:else}{vm.guestIP || '—'}{/if}
								</dd>
							</div>
						</dl>
					</section>

					<!-- Configuration & placement: desired config + where it runs. -->
					<section class="rounded border border-slate-200">
						<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">
							Configuration &amp; placement
						</h3>
						<dl class="divide-y divide-slate-100 text-[13px]">
							{@render field('Instance type', vm.instancetype ?? '')}
							{@render field('Preference', vm.preference ?? '')}
							{@render field('Node', vm.nodeName ?? '')}
							<div class="flex justify-between gap-3 px-3 py-1.5">
								<dt class="shrink-0 text-slate-500">Source</dt>
								<dd class="min-w-0 truncate text-right font-mono text-xs text-slate-600">{vm.sourceFile}</dd>
							</div>
						</dl>
					</section>
				</div>

				{#if vm.disks?.length || vm.networks?.length}
					<div class="mt-4 grid gap-4 md:grid-cols-2">
						{#if vm.disks?.length}
							<section class="rounded border border-slate-200">
								<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">
									Disks
								</h3>
								<ul class="divide-y divide-slate-100 px-3 text-[13px]">
									{#each vm.disks as d (d.name)}
										<li class="flex justify-between gap-3 py-1.5">
											<span class="text-slate-800">{d.name}</span>
											<span class="text-slate-400">{d.type}{d.size ? ` · ${d.size}` : ''}</span>
										</li>
									{/each}
								</ul>
							</section>
						{/if}
						{#if vm.networks?.length}
							<section class="rounded border border-slate-200">
								<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">
									Networks
								</h3>
								<ul class="divide-y divide-slate-100 px-3 text-[13px]">
									{#each vm.networks as n (n.name)}
										<li class="flex justify-between gap-3 py-1.5">
											<span class="text-slate-800">{n.name}</span>
											<span class="text-slate-400">{n.network}</span>
										</li>
									{/each}
								</ul>
							</section>
						{/if}
					</div>
				{/if}

				{#if vm.labels && Object.keys(vm.labels).length}
					<div class="mt-4">
						<h3 class="mb-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">Labels</h3>
						<div>
							{#each Object.entries(vm.labels) as [k, v] (k)}
								<span class="mr-1 mb-1 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-600">{k}={v}</span>
							{/each}
						</div>
					</div>
				{/if}

				{#if driftChanges && driftChanges.length > 0}
					<div class="mt-4 rounded border border-amber-200 bg-amber-50">
						<button
							onclick={() => (showDrift = !showDrift)}
							class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm font-medium text-amber-800"
						>
							<span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>
							Drift — cluster differs from git ({driftChanges.length})
							<span class="ml-auto text-amber-600">{#if showDrift}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}</span>
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
					<button onclick={() => (deleting = false)} class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
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
