<script lang="ts">
	import { ChevronDown, ChevronRight, X } from 'lucide-svelte';
	import { api, type EditRequest, type Options, type VM } from '$lib/api';

	let {
		vm,
		onclose,
		onstaged
	}: { vm: VM; onclose: () => void; onstaged: () => void } = $props();

	let options = $state<Options | null>(null);

	// The modal is mounted fresh per VM, so capturing the initial prop value to
	// seed the editable working copy is intentional.
	// svelte-ignore state_referenced_locally
	const seed = vm;
	let power = $state(seed.power);
	let cpuCores = $state<number | undefined>(seed.cpuCores);
	let memory = $state(seed.memory ?? '');
	let instancetype = $state(seed.instancetype ?? '');
	let preference = $state(seed.preference ?? '');
	let labelRows = $state(Object.entries(seed.labels ?? {}).map(([key, value]) => ({ key, value })));

	// Disks: existing (with a removed flag) + newly added blank disks.
	let disks = $state((seed.disks ?? []).map((d) => ({ ...d, removed: false, isNew: false })));
	let nics = $state((seed.networks ?? []).map((n) => ({ ...n, removed: false, isNew: false })));

	let saving = $state(false);
	let error = $state('');

	// Collapsible sections (vCenter expands them all by default).
	let open = $state({ compute: true, storage: true, network: true });

	let optionsError = $state('');
	$effect(() => {
		api
			.options()
			.then((o) => (options = o))
			.catch((e) => (optionsError = `Couldn't load cluster options: ${e}`));
	});

	function addNewDevice(kind: string) {
		if (kind === 'disk') {
			disks = [...disks, { name: `disk-${disks.length + 1}`, type: 'emptyDisk', size: '10Gi', removed: false, isNew: true }];
			open.storage = true;
		} else if (kind === 'network') {
			const first = options?.networks[0];
			nics = [...nics, { name: first ? first.name : 'net1', network: first ? `${first.namespace}/${first.name}` : '', removed: false, isNew: true }];
			open.network = true;
		}
	}

	function buildRequest(): EditRequest {
		const req: EditRequest = { sourceFile: vm.sourceFile };
		if (power !== vm.power) req.power = power;
		if (cpuCores !== vm.cpuCores) req.cpuCores = cpuCores;
		if (memory !== (vm.memory ?? '')) req.memory = memory;
		if (instancetype && instancetype !== (vm.instancetype ?? '')) req.instancetype = instancetype;
		if (preference && preference !== (vm.preference ?? '')) req.preference = preference;

		// Labels: upsert any changed/new, remove any deleted-from-original.
		const set: Record<string, string> = {};
		for (const r of labelRows) if (r.key.trim()) set[r.key.trim()] = r.value;
		const original = vm.labels ?? {};
		const setChanged: Record<string, string> = {};
		for (const [k, v] of Object.entries(set)) if (original[k] !== v) setChanged[k] = v;
		if (Object.keys(setChanged).length) req.setLabels = setChanged;
		const removedLabels = Object.keys(original).filter((k) => !(k in set));
		if (removedLabels.length) req.removeLabels = removedLabels;

		// Disks
		const addDisks = disks.filter((d) => d.isNew && !d.removed && d.name.trim());
		if (addDisks.length) req.addDisks = addDisks.map((d) => ({ name: d.name, size: d.size ?? '10Gi' }));
		const removeDisks = disks.filter((d) => !d.isNew && d.removed).map((d) => d.name);
		if (removeDisks.length) req.removeDisks = removeDisks;

		// Networks
		const addNetworks = nics.filter((n) => n.isNew && !n.removed && n.network);
		if (addNetworks.length) req.addNetworks = addNetworks.map((n) => ({ name: n.network! }));
		const removeNetworks = nics.filter((n) => !n.isNew && n.removed).map((n) => n.name);
		if (removeNetworks.length) req.removeNetworks = removeNetworks;

		return req;
	}

	const dirty = $derived.by(() => {
		const r = buildRequest();
		// Anything beyond the always-present sourceFile means a real change was made.
		return Object.keys(r).length > 1;
	});

	async function stage() {
		saving = true;
		error = '';
		try {
			await api.stageEdit(vm.namespace, vm.name, buildRequest());
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			saving = false;
		}
	}
</script>

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && onclose()}
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	role="presentation"
>
	<div class="flex max-h-[90vh] w-full max-w-2xl flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2 class="text-base font-semibold text-slate-800">Edit Settings — {vm.name}</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
		</header>

		<div class="min-h-0 flex-1 overflow-y-auto px-5 py-3">
			{#if optionsError}
				<div class="mb-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
					{optionsError} — the instance type / preference / network dropdowns may be empty.
				</div>
			{/if}
			<!-- Add new device toolbar -->
			<div class="mb-3 flex items-center gap-2">
				<span class="text-xs text-slate-400">Add new device:</span>
				<button onclick={() => addNewDevice('disk')} class="rounded border border-slate-300 px-2 py-0.5 text-xs hover:bg-slate-50">Hard disk</button>
				<button onclick={() => addNewDevice('network')} class="rounded border border-slate-300 px-2 py-0.5 text-xs hover:bg-slate-50">Network adapter</button>
			</div>

			<div class="space-y-3">
				<!-- Compute -->
				<section class="rounded border border-slate-200">
					<button
						onclick={() => (open.compute = !open.compute)}
						class="flex w-full items-center gap-2 bg-slate-50 px-3 py-1.5 text-left text-xs font-semibold tracking-wide text-slate-500 uppercase hover:bg-slate-100"
					>
						Compute
						<span class="ml-auto text-slate-400">{#if open.compute}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}</span>
					</button>
					{#if open.compute}
						<div class="grid grid-cols-2 gap-3 p-3 text-sm">
							<label class="block">
								<span class="text-slate-500">Power state</span>
								<select bind:value={power} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
									<option value="On">On</option>
									<option value="Off">Off</option>
									{#if power === 'Unknown'}<option value="Unknown">Unknown</option>{/if}
								</select>
							</label>
							<label class="block">
								<span class="text-slate-500">Instance type</span>
								<select bind:value={instancetype} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
									<option value="">— unchanged —</option>
									{#each options?.instancetypes ?? [] as it (it.name)}
										<option value={it.name}>{it.name} ({it.cpu} CPU / {it.memory})</option>
									{/each}
								</select>
							</label>
							<label class="block">
								<span class="text-slate-500">CPU cores</span>
								<input type="number" min="1" bind:value={cpuCores} class="mt-1 w-full rounded border border-slate-300 px-2 py-1" />
							</label>
							<label class="block">
								<span class="text-slate-500">Memory</span>
								<input bind:value={memory} placeholder="2Gi" class="mt-1 w-full rounded border border-slate-300 px-2 py-1" />
							</label>
							<label class="block">
								<span class="text-slate-500">Preference</span>
								<select bind:value={preference} class="mt-1 w-full rounded border border-slate-300 px-2 py-1">
									<option value="">— unchanged —</option>
									{#each options?.preferences ?? [] as p (p.name)}
										<option value={p.name}>{p.displayName || p.name}</option>
									{/each}
								</select>
							</label>
						</div>
					{/if}
				</section>

				<!-- Storage -->
				<section class="rounded border border-slate-200">
					<button
						onclick={() => (open.storage = !open.storage)}
						class="flex w-full items-center gap-2 bg-slate-50 px-3 py-1.5 text-left text-xs font-semibold tracking-wide text-slate-500 uppercase hover:bg-slate-100"
					>
						Storage <span class="font-normal text-slate-400 normal-case">({disks.filter((d) => !d.removed).length} disks)</span>
						<span class="ml-auto text-slate-400">{#if open.storage}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}</span>
					</button>
					{#if open.storage}
						<div class="p-3 text-sm">
							{#each disks as disk, i (i)}
								<div class="mb-1 flex items-center gap-2 {disk.removed ? 'opacity-40 line-through' : ''}">
									<span class="w-32 truncate text-slate-700">Hard disk {i + 1}</span>
									{#if disk.isNew}
										<input bind:value={disk.name} class="w-28 rounded border border-slate-300 px-2 py-0.5 text-xs" />
										<input bind:value={disk.size} class="w-20 rounded border border-slate-300 px-2 py-0.5 text-xs" />
									{:else}
										<span class="text-xs text-slate-500">{disk.name} ({disk.type}{disk.size ? ` · ${disk.size}` : ''})</span>
									{/if}
									<button onclick={() => (disk.removed = !disk.removed)} class="ml-auto text-xs {disk.removed ? 'text-blue-600' : 'text-red-500'}">
										{disk.removed ? 'undo' : 'remove'}
									</button>
								</div>
							{/each}
							{#if disks.filter((d) => !d.removed).length === 0}<p class="text-xs text-slate-400">No disks.</p>{/if}
						</div>
					{/if}
				</section>

				<!-- Network -->
				<section class="rounded border border-slate-200">
					<button
						onclick={() => (open.network = !open.network)}
						class="flex w-full items-center gap-2 bg-slate-50 px-3 py-1.5 text-left text-xs font-semibold tracking-wide text-slate-500 uppercase hover:bg-slate-100"
					>
						Network <span class="font-normal text-slate-400 normal-case">({nics.filter((n) => !n.removed).length} adapters)</span>
						<span class="ml-auto text-slate-400">{#if open.network}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}</span>
					</button>
					{#if open.network}
						<div class="p-3 text-sm">
							{#each nics as nic, i (i)}
								<div class="mb-1 flex items-center gap-2 {nic.removed ? 'opacity-40 line-through' : ''}">
									<span class="w-32 truncate text-slate-700">Network adapter {i + 1}</span>
									{#if nic.isNew}
										<select bind:value={nic.network} class="w-52 rounded border border-slate-300 px-2 py-0.5 text-xs">
											{#each options?.networks ?? [] as net (net.namespace + net.name)}
												<option value={`${net.namespace}/${net.name}`}>{net.namespace}/{net.name}</option>
											{/each}
										</select>
									{:else}
										<span class="text-xs text-slate-500">{nic.name} ({nic.network})</span>
									{/if}
									<button onclick={() => (nic.removed = !nic.removed)} class="ml-auto text-xs {nic.removed ? 'text-blue-600' : 'text-red-500'}">
										{nic.removed ? 'undo' : 'remove'}
									</button>
								</div>
							{/each}
							{#if nics.filter((n) => !n.removed).length === 0}<p class="text-xs text-slate-400">No adapters.</p>{/if}
						</div>
					{/if}
				</section>

				<!-- Labels -->
				<section class="rounded border border-slate-200">
					<div class="flex items-center justify-between bg-slate-50 px-3 py-1.5">
						<span class="text-xs font-semibold tracking-wide text-slate-500 uppercase">Labels</span>
						<button onclick={() => (labelRows = [...labelRows, { key: '', value: '' }])} class="text-xs text-blue-600 hover:underline">+ Add</button>
					</div>
					<div class="p-3 text-sm">
						{#each labelRows as row, i (i)}
							<div class="mb-1 flex gap-2">
								<input bind:value={row.key} placeholder="key" class="w-1/2 rounded border border-slate-300 px-2 py-0.5 text-xs" />
								<input bind:value={row.value} placeholder="value" class="w-1/2 rounded border border-slate-300 px-2 py-0.5 text-xs" />
								<button onclick={() => (labelRows = labelRows.filter((_, idx) => idx !== i))} aria-label="Remove label" class="text-red-500"><X size={14} /></button>
							</div>
						{/each}
						{#if labelRows.length === 0}<p class="text-xs text-slate-400">No labels.</p>{/if}
					</div>
				</section>
			</div>

			{#if error}
				<pre class="mt-3 rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
			{/if}
		</div>

		<footer class="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
			<span class="text-xs text-slate-400">Changes are staged into the changeset; review &amp; open a PR from “Changes”.</span>
			<button onclick={onclose} class="ml-auto rounded px-4 py-1.5 text-sm text-slate-600 hover:bg-slate-100">Cancel</button>
			<button onclick={stage} disabled={!dirty || saving} class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300">
				{saving ? 'Staging…' : 'Stage change'}
			</button>
		</footer>
	</div>
</div>
