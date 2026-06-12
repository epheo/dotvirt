<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, Unauthorized, type Options } from '$lib/api';

	// Content-library-lite: a read-only browser over the cluster's catalog —
	// boot images (DataSources), instance types, preferences, networks (NADs),
	// storage classes. The data is the wizard's own /api/options; dotvirt never
	// creates or edits these (platform objects).
	let { onclose }: { onclose: () => void } = $props();

	let options = $state<Options | null>(null);
	let error = $state('');

	type Kind = 'images' | 'instancetypes' | 'preferences' | 'networks' | 'storage';
	let kind = $state<Kind>('images');
	let picked = $state<string | null>(null); // selected item key within the kind

	const KINDS: { id: Kind; label: string }[] = [
		{ id: 'images', label: 'Boot images' },
		{ id: 'instancetypes', label: 'Instance types' },
		{ id: 'preferences', label: 'Preferences' },
		{ id: 'networks', label: 'Networks' },
		{ id: 'storage', label: 'Storage classes' }
	];

	$effect(() => {
		api
			.options()
			.then((o) => (options = o))
			.catch((e) => {
				if (e instanceof Unauthorized) return;
				error = String(e);
			});
	});

	function pick(k: Kind) {
		kind = k;
		picked = null;
	}

	// One uniform row shape per kind: key, title, a right-aligned fact, and the
	// detail fields shown when selected.
	type Row = { key: string; title: string; fact: string; detail: [string, string][] };
	const rows = $derived.by<Row[]>(() => {
		const o = options;
		if (!o) return [];
		switch (kind) {
			case 'images':
				return o.osImages.map((i) => ({
					key: `${i.namespace}/${i.name}`,
					title: i.name,
					fact: i.ready ? 'Ready' : 'Not ready',
					detail: [
						['Kind', 'DataSource (CDI)'],
						['Namespace', i.namespace],
						['Ready', i.ready ? 'Yes' : 'No'],
						['Used as', 'Root-disk source in the New VM wizard']
					]
				}));
			case 'instancetypes':
				return o.instancetypes.map((it) => ({
					key: it.name,
					title: it.name,
					fact: `${it.cpu} CPU / ${it.memory}`,
					detail: [
						['Kind', 'VirtualMachineClusterInstancetype'],
						['vCPUs', String(it.cpu)],
						['Memory', it.memory],
						['Used as', 'VM size (spec.instancetype)']
					]
				}));
			case 'preferences':
				return o.preferences.map((p) => ({
					key: p.name,
					title: p.displayName || p.name,
					fact: p.name,
					detail: [
						['Kind', 'VirtualMachineClusterPreference'],
						['Name', p.name],
						['Display name', p.displayName || '—'],
						['Used as', 'OS tuning (spec.preference)']
					]
				}));
			case 'networks':
				return o.networks.map((n) => ({
					key: `${n.namespace}/${n.name}`,
					title: n.name,
					fact: n.namespace,
					detail: [
						['Kind', 'NetworkAttachmentDefinition (Multus)'],
						['Namespace', n.namespace],
						['Reference', `${n.namespace}/${n.name}`],
						['Used as', 'Secondary VM network']
					]
				}));
			case 'storage':
				return o.storageClasses.map((sc) => ({
					key: sc.name,
					title: sc.name,
					fact: sc.default ? 'default' : '',
					detail: [
						['Kind', 'StorageClass'],
						['Cluster default', sc.default ? 'Yes' : 'No'],
						['Used as', 'dataVolume storage class for provisioned disks']
					]
				}));
		}
	});
	const pickedRow = $derived(rows.find((r) => r.key === picked) ?? null);
</script>

<aside class="flex h-full w-[28rem] flex-col border-l border-slate-300 bg-white shadow-xl">
	<header class="flex items-center justify-between border-b border-slate-200 px-4 py-3">
		<h2 class="text-base font-semibold text-slate-800">Catalog</h2>
		<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"
			><X size={18} /></button
		>
	</header>

	<div class="flex flex-wrap gap-1 border-b border-slate-200 px-3 py-2">
		{#each KINDS as k (k.id)}
			<button
				onclick={() => pick(k.id)}
				class="rounded px-2 py-0.5 text-xs {kind === k.id
					? 'bg-blue-100 font-medium text-blue-700'
					: 'text-slate-500 hover:bg-slate-100'}"
			>
				{k.label}
			</button>
		{/each}
	</div>

	<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
		{#if error}
			<p class="rounded bg-red-50 px-3 py-2 text-xs text-red-700">{error}</p>
		{:else if !options}
			<p class="py-6 text-center text-sm text-slate-400">Loading catalog…</p>
		{:else if rows.length === 0}
			<p class="py-6 text-center text-sm text-slate-400">None available on this cluster.</p>
		{:else}
			<ul class="divide-y divide-slate-100 rounded border border-slate-200 text-[13px]">
				{#each rows as r (r.key)}
					<li>
						<button
							onclick={() => (picked = picked === r.key ? null : r.key)}
							class="flex w-full items-baseline justify-between gap-3 px-3 py-1.5 text-left hover:bg-blue-50 {picked ===
							r.key
								? 'bg-blue-50'
								: ''}"
						>
							<span class="min-w-0 truncate font-medium text-slate-800">{r.title}</span>
							<span class="shrink-0 text-xs text-slate-400">{r.fact}</span>
						</button>
					</li>
				{/each}
			</ul>

			{#if pickedRow}
				<!-- Detail drawer for the selected catalog item. -->
				<section class="mt-3 rounded border border-slate-200">
					<h3
						class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
					>
						{pickedRow.title}
					</h3>
					<dl class="divide-y divide-slate-100 text-[13px]">
						{#each pickedRow.detail as [label, value] (label)}
							<div class="flex justify-between gap-3 px-3 py-1.5">
								<dt class="shrink-0 text-slate-500">{label}</dt>
								<dd class="min-w-0 truncate text-right text-slate-800">{value}</dd>
							</div>
						{/each}
					</dl>
				</section>
			{/if}
		{/if}
	</div>

	<footer class="border-t border-slate-200 px-4 py-2 text-xs text-slate-400">
		Read-only — these are platform objects; the New VM wizard consumes them.
	</footer>
</aside>
