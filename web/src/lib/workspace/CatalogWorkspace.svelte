<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import {
		CATALOG_KINDS,
		catalogContext,
		catalogKind,
		catalogRows,
		libraryLabel,
		type CatalogKind,
		type CatalogRow,
	} from '#lib/catalog.js';
	import { plural } from '#lib/format.js';
	import { networkKey } from '#lib/networks.js';
	import { hrefForScope, networkHref, storageHref } from '#lib/nav.js';
	import { DEFAULT_PAGE_SIZE, pageFor, pageSlice } from '#lib/paging.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { shortName } from '#lib/storage.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';
	import TabBar from '#lib/components/TabBar.svelte';
	import CatalogPanel from './catalog/CatalogPanel.svelte';

	// What new VMs are made from: templates kept in git first, then the
	// platform's boot images, sizes, preferences, networks and storage
	// classes. The kind is the tab (?kind=), the open item rides ?item= so the
	// tree, the table and a shared link agree. Images, networks and classes
	// stay one line each: their depth lives in Storage and Networking.
	const kind = $derived(catalogKind(page.url.searchParams.get('kind')));
	const picked = $derived(page.url.searchParams.get('item'));

	// Templates re-pull when a merged PR lands (tasksVersion): a template
	// committed through the app appears without a reload. The options catalog
	// re-pulls on entry after a boot-time failure, so it heals here.
	$effect(() => {
		inventory.tasksVersion;
		catalog.load();
	});
	$effect(() => {
		inventory.loadOptions();
	});

	const rowsOf = (k: CatalogKind) =>
		catalogRows(k, catalog.templates, inventory.options, inventory.networks);
	const rows = $derived(rowsOf(kind));
	const tabs = $derived(CATALOG_KINDS.map((k) => ({ ...k, count: rowsOf(k.id)?.length })));
	const label = $derived(CATALOG_KINDS.find((k) => k.id === kind)!.label);
	const context = $derived(catalogContext(catalog.templates, inventory.options));
	const invalid = $derived(catalog.templates?.filter((t) => t.error).length ?? 0);
	const error = $derived(kind === 'templates' ? catalog.error : inventory.optionsError);
	// A VM stages into a namespace the caller authors in; the sheet then asks
	// vmGate.create of the one picked.
	const canCreate = $derived(inventory.namespaces.length > 0);

	// An item picked from the tree lands on the page that holds it.
	const keys = $derived(rows?.map((r) => r.key) ?? []);
	const size = $derived(Number(page.url.searchParams.get('per')) || DEFAULT_PAGE_SIZE);
	const s = $derived(
		pageSlice(
			keys.length,
			Number(page.url.searchParams.get('page')) || pageFor(keys, picked, size),
			size,
		),
	);
	const pageRows = $derived(rows?.slice(s.start, s.end) ?? []);
	const open = $derived(rows?.find((r) => r.key === picked) ?? null);

	// Paging and the open item are replaceState, like tabs: back never walks
	// them. Two moves in one handler compose (see StorageWorkspace).
	let pending: URLSearchParams | null = null;
	function go(params: Record<string, string | undefined>) {
		const q = new URLSearchParams(pending ?? page.url.search);
		for (const [k, v] of Object.entries(params)) {
			if (v) q.set(k, v);
			else q.delete(k);
		}
		q.set('kind', kind);
		pending = q;
		goto(`/catalog?${q}`, { replace: true, reset: false }).finally(() => {
			if (pending === q) pending = null;
		});
	}
	const toggle = (key: string) => go({ item: picked === key ? undefined : key });

	// Disks made from each image, counted from the live inventory with no read
	// of its own; where they live and their snapshots stay on Storage.
	const disksOf = $derived(new Map(storage.images.map((u) => [u.image, u.disks])));
	// A network opens its own page; an attachment no known network owns has none.
	const netHref = (r: CatalogRow) =>
		r.network?.known ? networkHref(networkKey(r.network.known)) : '';
	const classHref = (r: CatalogRow) => hrefForScope({ kind: 'storage', storageClass: r.title });

	const fromTemplate = (r: CatalogRow) =>
		(ui.modal = { kind: 'deployTemplate', library: r.template!.library, template: r.title });
	const fromImage = (r: CatalogRow) =>
		(ui.modal = { kind: 'newVM', namespaces: null, image: r.key });

	// Boot images usually share one namespace: said once, not on every row.
	const imageNS = $derived([
		...new Set((inventory.options?.osImages ?? []).map((i) => i.namespace)),
	]);
	const PLATFORM = 'Managed by the cluster platform, not dotvirt';
	const META: Record<CatalogKind, string> = $derived({
		templates: 'In git, under templates/ in each library repo',
		images:
			imageNS.length === 1
				? `What a new VM boots from, kept in ${imageNS[0]}`
				: 'What a new VM boots from',
		instancetypes: PLATFORM,
		preferences: PLATFORM,
		networks: 'What a VM can attach a second NIC to',
		storage: PLATFORM,
	});
	const LINK: Partial<Record<CatalogKind, { label: string; href: string }>> = {
		images: { label: 'Usage and uploads on the Storage Images tab', href: storageHref('images') },
		networks: { label: 'All networks in Networking', href: '/networking?tab=networks' },
		storage: { label: 'Capacity on the Storage Classes tab', href: storageHref('classes') },
	};
	const NAME: Record<CatalogKind, string> = {
		templates: 'Template',
		images: 'Image',
		instancetypes: 'Instance type',
		preferences: 'Preference',
		networks: 'Network',
		storage: 'Class',
	};
	const FACT: Partial<Record<CatalogKind, string>> = {
		instancetypes: 'Size',
		preferences: 'Details',
		networks: 'Details',
		storage: 'Default',
	};
	const creates = $derived(canCreate && (kind === 'templates' || kind === 'images'));
	const acts = $derived(creates || kind === 'networks' || kind === 'storage');
	const cols = $derived((kind === 'templates' ? 4 : kind === 'images' ? 3 : 2) + (acts ? 1 : 0));
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5';
</script>

{#snippet empty()}
	{#if kind === 'templates'}
		No templates yet. Clone a VM to one from its menu (Clone to Template), or commit
		VirtualMachineTemplate manifests under templates/ in a library repo.
	{:else if kind === 'images'}
		No boot images visible to you.
		<a class="text-accent-ink underline underline-offset-2" href={storageHref('images')}
			>Add one on the Storage Images tab</a
		>
	{:else if kind === 'networks'}
		No network attachments visible to you.
		<a class="text-accent-ink underline underline-offset-2" href="/networking?tab=networks"
			>Create a network in Networking</a
		>
	{:else}
		None on this cluster. A cluster admin installs them.
	{/if}
{/snippet}

{#snippet cells(r: CatalogRow, current: boolean)}
	<td class="{TD} pl-card">
		<button
			class="text-left font-mono font-medium hover:text-accent-ink"
			aria-expanded={current}
			onclick={(e) => {
				e.stopPropagation();
				toggle(r.key);
			}}>{r.title}</button
		>
		{#if r.template?.error}
			<span class="ml-1.5"
				><StatusPill tone="warn" label="Does not parse" title={r.template.error} /></span
			>
		{/if}
		{#if r.template?.description}
			<span class="block text-xs leading-4 text-ink-faint">{r.template.description}</span>
		{:else if r.image && imageNS.length > 1}
			<span class="block text-xs leading-4 text-ink-faint">In {r.image.namespace}</span>
		{/if}
	</td>
	{#if r.template}
		{@const t = r.template}
		<td class="{TD} text-ink-muted">{libraryLabel(t.library)}</td>
		<td class="{TD} font-mono text-[13px] text-ink-muted">{t.instancetype || '-'}</td>
		<td class="{TD} font-mono text-[13px] text-ink-muted">{t.image ? shortName(t.image) : '-'}</td>
	{:else if r.image}
		{@const disks = disksOf.get(r.key) ?? 0}
		<td class={TD}>
			<span class={r.image.ready ? 'text-ink-soft' : 'text-warn-ink'}>{r.fact}</span>
		</td>
		<td class="{TD} text-right">
			{#if disks}
				<a
					class="text-accent-ink hover:underline"
					href={storageHref('disks', { image: r.key })}
					onclick={(e) => e.stopPropagation()}>{plural(disks, 'disk')}</a
				>
			{:else}<span class="text-ink-faint">None yet</span>{/if}
		</td>
	{:else}
		<td class="{TD} text-ink-muted">{r.fact}</td>
	{/if}
	{#if acts}
		<td class="{TD} pr-card">
			<div class="flex justify-end gap-1.5">
				{#if r.template && creates}
					<Button
						variant="outline"
						size="sm"
						aria-label="Create VM from {r.title}"
						disabled={!!r.template.error}
						title={r.template.error ? 'It does not parse. Fix its file first.' : ''}
						onclick={(e) => {
							e.stopPropagation();
							fromTemplate(r);
						}}>Create VM</Button
					>
				{:else if r.image && creates}
					<Button
						variant="outline"
						size="sm"
						aria-label="Create VM from {r.title}"
						disabled={!r.image.ready}
						title={r.image.ready ? '' : 'The image is still importing'}
						onclick={(e) => {
							e.stopPropagation();
							fromImage(r);
						}}>Create VM</Button
					>
				{:else if r.network}
					{@const href = netHref(r)}
					{#if href}
						<a
							class="text-accent-ink hover:underline"
							aria-label="Open {r.title} in Networking"
							{href}
							onclick={(e) => e.stopPropagation()}>Open in Networking</a
						>
					{/if}
				{:else if r.storageClass}
					<a
						class="text-accent-ink hover:underline"
						aria-label="Open {r.title} in Storage"
						href={classHref(r)}
						onclick={(e) => e.stopPropagation()}>Open in Storage</a
					>
				{/if}
			</div>
		</td>
	{/if}
{/snippet}

<div class="flex min-h-0 flex-1 flex-col bg-canvas">
	<div class="flex flex-col gap-2 px-6 pt-5">
		<Breadcrumb trail={[{ label: 'Catalog', href: '/catalog' }, { label }]} />
		<PageHeader
			title="Catalog"
			status={invalid
				? {
						tone: 'warn',
						text: `${plural(invalid, 'template')} ${invalid === 1 ? 'does' : 'do'} not parse`,
					}
				: undefined}
			{context}
		/>
		<TabBar {tabs} active={kind} href={(k) => `?kind=${k}`} />
	</div>

	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div
		class="min-h-0 flex-1 overflow-y-auto px-6 pt-4 pb-7"
		role="region"
		aria-label="Catalog, {label}"
		tabindex="0"
	>
		{#if error}
			<div class="mb-4 flex flex-col items-start gap-2">
				<ErrorNote {error} class="w-full" />
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (kind === 'templates' ? catalog.load() : inventory.loadOptions(true))}
					>Try again</Button
				>
			</div>
		{/if}

		<!-- A failed read with nothing to show is the error alone, never a
		     table that reads as loading. -->
		{#if !error || rows}
			<div class="flex flex-wrap items-start gap-4">
				<Card class="flex-[2_1_640px]" title={label} meta={META[kind]} link={LINK[kind]} flush>
					<div class="overflow-x-auto">
						<table class="w-full border-collapse text-sm">
							<thead>
								<tr>
									<th scope="col" class="{TH} pl-card">{NAME[kind]}</th>
									{#if kind === 'templates'}
										<th scope="col" class={TH}>Library</th>
										<th scope="col" class={TH}>Instance type</th>
										<th scope="col" class={TH}>Boot image</th>
									{:else if kind === 'images'}
										<th scope="col" class={TH}>State</th>
										<th scope="col" class="{TH} text-right">Disks made from it</th>
									{:else}
										<th scope="col" class={TH}>{FACT[kind]}</th>
									{/if}
									{#if acts}
										<th scope="col" class="{TH} pr-card"><span class="sr-only">Actions</span></th>
									{/if}
								</tr>
							</thead>
							<tbody class="tabular-nums">
								{#each pageRows as r (r.key)}
									{@const current = r.key === picked}
									<tr
										class="cursor-pointer {current
											? 'bg-select shadow-[inset_3px_0_0_var(--color-accent)]'
											: 'hover:bg-select-soft'}"
										onclick={() => toggle(r.key)}
									>
										{@render cells(r, current)}
									</tr>
								{:else}
									<tr>
										<td colspan={cols} class="px-card py-6 text-center text-ink-faint">
											{#if rows === null}
												{kind === 'templates'
													? 'Reading the template libraries.'
													: 'Reading the cluster catalog.'}
											{:else}
												{@render empty()}
											{/if}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
					<Pager
						class="border-t border-line px-card py-3"
						total={s.total}
						bind:page={() => s.page, (n) => go({ page: n > 1 ? String(n) : undefined })}
						bind:size={
							() => size, (n) => go({ per: n === DEFAULT_PAGE_SIZE ? undefined : String(n) })
						}
					/>
					{#if creates}
						<div
							class="flex items-start gap-2 border-t border-line-soft px-card py-3.5 text-xs leading-5 text-ink-faint"
						>
							<StageChip kind="pr" />
							<span class="min-w-0"
								>Create VM opens the create sheet with the {kind === 'templates'
									? 'template'
									: 'image'} picked. Staging it opens a pull request in the chosen project.</span
							>
						</div>
					{/if}
				</Card>

				{#if open}
					{@const disks = open.image ? (disksOf.get(open.key) ?? 0) : 0}
					{@const net = open.network ? netHref(open) : ''}
					<CatalogPanel
						row={open}
						oncreate={!canCreate
							? undefined
							: open.template
								? () => fromTemplate(open)
								: open.image
									? () => fromImage(open)
									: undefined}
						createOff={open.template?.error
							? 'It does not parse. Fix its file first.'
							: open.image && !open.image.ready
								? 'The image is still importing.'
								: ''}
						disks={disks
							? {
									label: `Show the ${plural(disks, 'disk')}`,
									href: storageHref('disks', { image: open.key }),
								}
							: undefined}
						elsewhere={net
							? { label: 'Open in Networking', href: net }
							: open.storageClass
								? { label: 'Open in Storage', href: classHref(open) }
								: open.image
									? { label: 'Open in Storage', href: storageHref('images', { image: open.key }) }
									: undefined}
						onclose={() => go({ item: undefined })}
					/>
				{/if}
			</div>
		{/if}
	</div>
</div>
