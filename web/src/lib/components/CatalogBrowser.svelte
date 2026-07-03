<script lang="ts">
	import { page } from '$app/state';
	import { api, Unauthorized, type Options } from '$lib/api';
	import TabBar from './TabBar.svelte';

	// Content-library-lite: a read-only browser over the cluster's catalog —
	// boot images (DataSources), instance types, preferences, networks (NADs),
	// storage classes. The data is the wizard's own /api/options; dotvirt never
	// creates or edits these (platform objects). The kind rides ?kind= so a
	// catalog tab is deep-linkable like every other tab.
	let options = $state<Options | null>(null);
	let error = $state('');

	type Kind = 'images' | 'instancetypes' | 'preferences' | 'networks' | 'storage';
	let picked = $state<string | null>(null); // selected item key within the kind

	const KINDS: { id: Kind; label: string }[] = [
		{ id: 'images', label: 'Boot images' },
		{ id: 'instancetypes', label: 'Instance types' },
		{ id: 'preferences', label: 'Preferences' },
		{ id: 'networks', label: 'Networks' },
		{ id: 'storage', label: 'Storage classes' }
	];
	const kind = $derived.by<Kind>(() => {
		const k = page.url.searchParams.get('kind');
		return KINDS.some((x) => x.id === k) ? (k as Kind) : 'images';
	});
	$effect(() => {
		kind;
		picked = null;
	});

	$effect(() => {
		api
			.options()
			.then((o) => (options = o))
			.catch((e) => {
				if (e instanceof Unauthorized) return;
				error = String(e);
			});
	});

	// One uniform row shape per kind: key, title, a right-aligned fact, and the
	// detail fields shown when selected.
	type Row = { key: string; title: string; fact: string; detail: [string, string][] };
	const rows = $derived.by<Row[]>(() => {
		const o = options;
		if (!o) return [];
		switch (kind) {
			case 'images':
				return (o.osImages ?? []).map((i) => ({
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
				return (o.instancetypes ?? []).map((it) => ({
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
				return (o.preferences ?? []).map((p) => ({
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
				return (o.networks ?? []).map((n) => ({
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
				return (o.storageClasses ?? []).map((sc) => ({
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

<TabBar class="border-b border-line px-4" tabs={KINDS} active={kind} href={(k) => `?kind=${k}`} />

<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
	<div class="max-w-3xl">
		{#if error}
			<p class="rounded bg-red-50 px-3 py-2 text-xs text-red-700">{error}</p>
		{:else if !options}
			<p class="py-6 text-center text-sm text-slate-400">Loading catalog…</p>
		{:else if rows.length === 0}
			<p class="py-6 text-center text-sm text-slate-400">None available on this cluster.</p>
		{:else}
			<ul class="divide-y divide-slate-100 rounded border border-line text-[13px]">
				{#each rows as r (r.key)}
					<li>
						<button
							onclick={() => (picked = picked === r.key ? null : r.key)}
							class="flex w-full items-baseline justify-between gap-3 px-3 py-1.5 text-left hover:bg-select-soft {picked ===
							r.key
								? 'bg-select hover:bg-select'
								: ''}"
						>
							<span class="min-w-0 truncate font-medium text-ink">{r.title}</span>
							<span class="shrink-0 text-xs text-ink-faint">{r.fact}</span>
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
</div>

<footer class="border-t border-line px-4 py-2 text-xs text-ink-faint">
	Read-only — these are platform objects; the New VM wizard consumes them.
</footer>
