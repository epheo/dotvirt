<script lang="ts">
	import { page } from '$app/state';
	import { api, Unauthorized, type Options, type Template } from '$lib/api';
	import { ui } from '$lib/state/ui.svelte';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';

	// The Content Library: VM templates (deployable, git-backed) first, then a
	// read-only browser over the cluster's catalog — boot images (DataSources),
	// instance types, preferences, networks (NADs), storage classes. Catalog
	// kinds are the wizard's own /api/options; templates come from the library
	// repos via /api/templates. The kind rides ?kind= so a catalog tab is
	// deep-linkable like every other tab.
	let options = $state<Options | null>(null);
	let templates = $state<Template[] | null>(null);
	let error = $state('');

	type Kind = 'templates' | 'images' | 'instancetypes' | 'preferences' | 'networks' | 'storage';
	let picked = $state<string | null>(null); // selected item key within the kind

	const KINDS: { id: Kind; label: string }[] = [
		{ id: 'templates', label: 'VM Templates' },
		{ id: 'images', label: 'Boot images' },
		{ id: 'instancetypes', label: 'Instance types' },
		{ id: 'preferences', label: 'Preferences' },
		{ id: 'networks', label: 'Networks' },
		{ id: 'storage', label: 'Storage classes' }
	];
	const kind = $derived.by<Kind>(() => {
		const k = page.url.searchParams.get('kind');
		return KINDS.some((x) => x.id === k) ? (k as Kind) : 'templates';
	});
	const kindLabel = $derived(KINDS.find((k) => k.id === kind)!.label);
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
		api
			.templates()
			.then((t) => (templates = t.templates))
			.catch((e) => {
				if (e instanceof Unauthorized) return;
				error = String(e);
			});
	});

	// The shared library reads as vCenter's subscribed Content Library.
	const libraryLabel = (lib: string) => (lib === 'platform' ? 'Shared library' : lib);

	// One uniform row shape per kind: key, title, a right-aligned fact, and the
	// detail fields shown when selected. Template rows also carry the template so
	// the detail pane can render parameters + Deploy.
	type Row = {
		key: string;
		title: string;
		fact: string;
		detail: [string, string][];
		template?: Template;
	};
	const rows = $derived.by<Row[]>(() => {
		if (kind === 'templates') {
			return (templates ?? []).map((t) => ({
				key: `${t.library}/${t.name}`,
				title: t.name,
				fact: t.error
					? 'Invalid'
					: [libraryLabel(t.library), t.instancetype].filter(Boolean).join(' · '),
				detail: [
					['Kind', 'VirtualMachineTemplate (git)'],
					['Library', libraryLabel(t.library)],
					['Description', t.description || '—'],
					['Instance type', t.instancetype || '—'],
					['Preference', t.preference || '—'],
					['Source file', t.sourceFile],
					...(t.error ? ([['Error', t.error]] as [string, string][]) : [])
				],
				template: t
			}));
		}
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

<Breadcrumb trail={[{ label: 'Catalog', href: '/catalog' }, { label: kindLabel }]} />

<div class="flex min-h-0 flex-1">
	<div class="min-h-0 flex-1 overflow-y-auto">
		{#if error}
			<p class="m-4 rounded bg-red-50 px-3 py-2 text-xs text-red-700">{error}</p>
		{:else if kind === 'templates' ? !templates : !options}
			<p class="py-6 text-center text-sm text-ink-faint">Loading catalog…</p>
		{:else if rows.length === 0}
			<p class="py-6 text-center text-sm text-ink-faint">
				{kind === 'templates'
					? 'No templates yet — save one from a VM (Clone to Template) or commit VirtualMachineTemplate manifests under templates/.'
					: 'None available on this cluster.'}
			</p>
		{:else}
			<table class="w-full text-left text-[13px]">
				<thead class="border-b border-line text-xs text-ink-muted">
					<tr>
						<th class="px-4 py-2 font-medium">Name</th>
						<th class="px-4 py-2 font-medium">Details</th>
					</tr>
				</thead>
				<tbody>
					{#each rows as r (r.key)}
						<tr
							onclick={() => (picked = picked === r.key ? null : r.key)}
							class="cursor-pointer border-b border-slate-100 hover:bg-select-soft {picked === r.key
								? 'bg-select hover:bg-select'
								: ''}"
						>
							<td class="px-4 py-1.5 font-medium text-ink">{r.title}</td>
							<td class="px-4 py-1.5 text-ink-muted">{r.fact}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>

	{#if pickedRow}
		<!-- Detail pane for the selected catalog item. -->
		<aside class="w-80 shrink-0 overflow-y-auto border-l border-line">
			<h3
				class="border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
			>
				{pickedRow.title}
			</h3>
			<dl class="divide-y divide-slate-100 text-[13px]">
				{#each pickedRow.detail as [label, value] (label)}
					<div class="flex justify-between gap-3 px-3 py-1.5">
						<dt class="shrink-0 text-ink-muted">{label}</dt>
						<dd class="min-w-0 truncate text-right text-ink" title={value}>{value}</dd>
					</div>
				{/each}
			</dl>
			{#if pickedRow.template}
				{@const t = pickedRow.template}
				{#if t.parameters?.length}
					<h4
						class="border-y border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
					>
						Parameters
					</h4>
					<ul class="divide-y divide-slate-100 text-[13px]">
						{#each t.parameters as p (p.name)}
							<li class="px-3 py-1.5">
								<div class="flex justify-between gap-3">
									<span class="font-mono text-xs text-ink">{p.name}</span>
									<span class="text-xs text-ink-muted">
										{p.generate
											? 'generated'
											: p.value
												? p.value
												: p.required
													? 'required'
													: 'optional'}
									</span>
								</div>
								{#if p.description}<p class="mt-0.5 text-xs text-ink-faint">{p.description}</p>{/if}
							</li>
						{/each}
					</ul>
				{/if}
				<div class="flex gap-2 border-t border-line p-3">
					{#if !t.error}
						<button
							onclick={() =>
								(ui.modal = { kind: 'deployTemplate', library: t.library, template: t.name })}
							class="flex-1 rounded bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
						>
							Deploy…
						</button>
					{/if}
					<button
						onclick={() => (ui.modal = { kind: 'editTemplate', template: t })}
						class="flex-1 rounded border border-line px-3 py-1.5 text-xs font-medium text-ink hover:bg-inset"
					>
						Edit…
					</button>
				</div>
			{/if}
		</aside>
	{/if}
</div>

<footer class="border-t border-line px-4 py-2 text-xs text-ink-faint">
	{kind === 'templates'
		? 'Templates live in git (templates/ in each library repo); deploying stages a VM into Changes — it applies when the PR merges.'
		: 'Read-only — these are platform objects; the New VM wizard consumes them.'}
</footer>
