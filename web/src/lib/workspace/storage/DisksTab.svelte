<script lang="ts">
	import { page } from '$app/state';
	import { ArrowLeftRight, Camera, ChevronDown, ChevronUp, Funnel, Plus, X } from '@lucide/svelte';
	import { api, Unauthorized, type VM } from '#lib/api.js';
	import { vmGate } from '#lib/gates.js';
	import { bulkOutcome } from '#lib/bulk.js';
	import { vmHref } from '#lib/nav.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import {
		ANY_SOURCE,
		FLAG_LABEL,
		SOURCE_WORD,
		VALUE_FILTERS,
		VALUE_LABEL,
		applyFilters,
		filtersFromQuery,
		filtersToQuery,
		hasFilters,
		moveConsequences,
		moveNote,
		num,
		planBulkMove,
		planBulkSnapshot,
		repeatedNames,
		settleEach,
		ago,
		shortName,
		sortRows,
		sourceOf,
		sourceTail,
		sourceText,
		suggestTarget,
		type DiskFilters,
		type DiskRow,
		type FlagFilter,
		type ValueFilter,
	} from '#lib/storage.js';
	import { quantity } from '#lib/format.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import DiskPanel from './DiskPanel.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';

	// The Disks tab: a tile's filter becomes a paged table where a bulk move or
	// snapshot shows its consequences before it runs; one disk opens beside it.
	let {
		rows,
		go,
	}: {
		rows: DiskRow[];
		go: (params: Record<string, string | undefined>, opts?: { replace?: boolean }) => void;
	} = $props();

	const q = $derived(new URLSearchParams(page.url.search));
	const filters = $derived(filtersFromQuery(q));
	// The project scope is the tree's too: its chip and the tree row are one
	// query parameter.
	const shown = $derived(applyFilters(rows, filters));
	const total = $derived(
		rows.filter((r) => r.ephemeral === filters.flags.includes('nodata')).length,
	);
	const desc = $derived(q.get('sort') === 'vm-desc');
	const sorted = $derived(sortRows(shown, 'vm', desc));
	const repeated = $derived(
		repeatedNames(
			shown,
			(r) => r.vm.namespace,
			(r) => r.vm.name,
		),
	);
	const size = $derived(Number(q.get('per')) || DEFAULT_PAGE_SIZE);
	const s = $derived(pageSlice(sorted.length, Number(q.get('page')) || 1, size));
	const pageRows = $derived(sorted.slice(s.start, s.end));
	const openKey = $derived(q.get('disk'));
	const open = $derived(openKey ? (rows.find((r) => r.key === openKey) ?? null) : null);

	function setFilters(next: DiskFilters) {
		const out = filtersToQuery(next, q);
		go(
			{
				f: out.get('f') ?? undefined,
				project: out.get('project') ?? undefined,
				class: out.get('class') ?? undefined,
				host: out.get('host') ?? undefined,
				image: out.get('image') ?? undefined,
				source: out.get('source') ?? undefined,
				q: out.get('q') ?? undefined,
				page: undefined,
			},
			{ replace: true },
		);
	}
	const dropFlag = (f: FlagFilter) =>
		setFilters({ ...filters, flags: filters.flags.filter((x) => x !== f) });
	const dropValue = (k: ValueFilter) => setFilters({ ...filters, [k]: undefined });
	function addFlag(f: FlagFilter) {
		// Keeping no data and every other flag describe different rows.
		const flags =
			f === 'nodata' ? ['nodata' as const] : [...filters.flags.filter((x) => x !== 'nodata'), f];
		setFilters({ ...filters, flags });
	}

	// --- the Add filter menu ---
	let adding = $state(false);
	const valueChoices = $derived.by(() => {
		const count = (pick: (r: DiskRow) => string) => {
			const m = new Map<string, number>();
			for (const r of rows) if (!r.ephemeral && pick(r)) m.set(pick(r), (m.get(pick(r)) ?? 0) + 1);
			return [...m.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
		};
		const sources = count((r) => r.source);
		const imported = sources.reduce((n, [, c]) => n + c, 0);
		return {
			project: count((r) => r.project),
			class: count((r) => r.storageClass),
			host: count((r) => r.host),
			image: count((r) => r.image),
			source: imported ? [[ANY_SOURCE, imported], ...sources] : [],
		} satisfies Record<ValueFilter, [string, number][]>;
	});
	function valueText(k: ValueFilter, v: string): string {
		if (k === 'image') return shortName(v);
		if (k === 'source') return v === ANY_SOURCE ? 'any registry or URL' : sourceText(sourceOf(v));
		return v;
	}

	// --- selection: row keys, cleared when the filtered set changes ---
	let selected = $state<Set<string>>(new Set());
	const viewKey = $derived(
		['f', 'project', 'class', 'host', 'image', 'source', 'q'].map((k) => q.get(k)).join('|'),
	);
	$effect(() => {
		void viewKey;
		selected = new Set();
	});
	const pageKeys = $derived(pageRows.map((r) => r.key));
	const onPage = $derived(pageKeys.filter((k) => selected.has(k)).length);
	const allOnPage = $derived(pageKeys.length > 0 && onPage === pageKeys.length);
	const selectable = $derived(!filters.flags.includes('nodata'));
	function togglePage() {
		const next = new Set(selected);
		for (const k of pageKeys) {
			if (allOnPage) next.delete(k);
			else next.add(k);
		}
		selected = next;
	}
	function toggle(key: string) {
		const next = new Set(selected);
		if (next.has(key)) next.delete(key);
		else next.add(key);
		selected = next;
	}
	const picked = $derived(shown.filter((r) => selected.has(r.key)));

	// --- the bulk action: snapshot when the view is about stale snapshots, else
	// a move to a shared class ---
	const mode = $derived(filters.flags.includes('nosnap') ? 'snapshot' : 'move');
	const target = $derived(suggestTarget(storage.classes ?? []));
	// Each VM's own right: a move stages a VM change, a snapshot runs under the
	// caller's token. The rest of the selection acts; none allowed holds it back.
	const movable = $derived(picked.filter((r) => vmGate.update(r.vm.namespace).ok));
	const snappable = $derived(picked.filter((r) => vmGate.snapshotTake(r.vm.namespace).ok));
	const moveWhy = $derived(
		picked.length && !movable.length ? vmGate.update(picked[0].vm.namespace).why : '',
	);
	const snapWhy = $derived(
		picked.length && !snappable.length ? vmGate.snapshotTake(picked[0].vm.namespace).why : '',
	);
	const move = $derived(
		planBulkMove(movable, target, {
			classes: storage.classes ?? [],
			staged: drafts.stagedByKey,
			rows: storage.rows,
		}),
	);
	const snap = $derived(planBulkSnapshot(snappable, storage.classes ?? []));

	const bulkOp = action({ toast: ui.toastError });

	// One call per VM in parallel; a per-VM failure never aborts the batch and
	// failed VMs' disks stay selected for a retry.
	async function runEach(
		items: { vm: VM; keys: string[] }[],
		call: (vm: VM) => Promise<unknown>,
		verb: string,
	) {
		const results = await settleEach(items, (i) => call(i.vm));
		const denied = results.find(
			(r) => r.status === 'rejected' && r.reason instanceof Unauthorized,
		) as PromiseRejectedResult | undefined;
		if (denied) throw denied.reason;
		const out = bulkOutcome(
			verb,
			items.length,
			items.map((i) => i.vm),
			results,
		);
		const failedVMs = out.keep;
		selected = new Set(
			items.filter((i) => failedVMs.has(`${i.vm.namespace}/${i.vm.name}`)).flatMap((i) => i.keys),
		);
		return out;
	}

	function stageMove() {
		if (bulkOp.busy || !move.edits.length) return;
		bulkOp.run(async () => {
			const out = await runEach(
				move.edits.map((e) => ({ vm: e.vm, keys: e.disks.map((d) => d.key) })),
				(vm) => {
					const e = move.edits.find((x) => x.vm === vm)!;
					return api.stageEdit(vm.namespace, vm.name, { sourceFile: vm.sourceFile, ...e.edit });
				},
				`Moves to ${target} staged for`,
			);
			if (out.staged > 0) ui.toastStaged(out.msg, { kind: out.failed ? 'error' : 'success' });
			else ui.showToast(out.msg, { kind: 'error' });
		});
	}

	function snapshotNow() {
		if (bulkOp.busy || !snap.vms.length) return;
		bulkOp.run(async () => {
			const out = await runEach(
				snap.vms.map((vm) => ({
					vm,
					keys: picked.filter((r) => r.vm === vm).map((r) => r.key),
				})),
				(vm) => api.takeSnapshot(vm.namespace, vm.name),
				'Snapshots started for',
			);
			ui.showToast(out.msg, { kind: out.failed ? 'error' : 'success' });
			await storage.refreshSnapshots();
		});
	}

	const lastSnap = (r: DiskRow) => r.snapshots[0]?.created;
	const TH =
		'sticky top-0 z-[1] border-b border-line bg-canvas/60 px-2 py-[9px] text-left text-xs font-bold whitespace-nowrap text-ink-muted backdrop-blur';
</script>

<div class="flex flex-wrap items-start gap-4">
	<section
		aria-labelledby="disks-h"
		class="flex min-w-0 flex-[2_1_640px] flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
	>
		<h2 id="disks-h" class="sr-only">Disks</h2>
		<div
			class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
		>
			<div class="flex flex-wrap items-center gap-2">
				<Funnel size={16} class="text-ink-muted" />
				{#each filters.flags as f (f)}
					<span
						class="inline-flex h-7 items-center gap-1.5 rounded-md border border-accent bg-panel pr-1 pl-2.5 text-[13px] text-ink"
						>{FLAG_LABEL[f]}<button
							class="flex size-[22px] items-center justify-center rounded text-ink-muted hover:bg-inset"
							aria-label="Remove filter: {FLAG_LABEL[f]}"
							onclick={() => dropFlag(f)}><X size={14} /></button
						></span
					>
				{/each}
				{#each [...VALUE_FILTERS, 'q' as const] as k (k)}
					{@const v = filters[k]}
					{#if v}
						<span
							class="inline-flex h-7 items-center gap-1.5 rounded-md border border-accent bg-panel pr-1 pl-2.5 text-[13px] text-ink"
							><span class="text-ink-muted">{k === 'q' ? 'Matches' : VALUE_LABEL[k]}</span>{k ===
							'q'
								? v
								: valueText(k, v)}<button
								class="flex size-[22px] items-center justify-center rounded text-ink-muted hover:bg-inset"
								aria-label="Remove filter: {k === 'q'
									? 'matches'
									: VALUE_LABEL[k].toLowerCase()} {v}"
								onclick={() =>
									k === 'q' ? setFilters({ ...filters, q: undefined }) : dropValue(k)}
								><X size={14} /></button
							></span
						>
					{/if}
				{/each}
				<span class="relative">
					<Button
						variant="secondary"
						size="sm"
						aria-expanded={adding}
						onclick={() => (adding = !adding)}><Plus size={14} />Add filter</Button
					>
					{#if adding}
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<div
							class="absolute top-full left-0 z-20 mt-1 flex w-72 flex-col gap-2 rounded-lg border border-line bg-panel p-3 text-[13px] shadow-lg"
							onkeydown={(e) => e.key === 'Escape' && (adding = false)}
						>
							<div class="text-xs font-bold text-ink-muted">Show only disks that</div>
							{#each Object.keys(FLAG_LABEL) as FlagFilter[] as f (f)}
								<label class="flex items-center gap-2">
									<input
										type="checkbox"
										checked={filters.flags.includes(f)}
										onchange={(e) => (e.currentTarget.checked ? addFlag(f) : dropFlag(f))}
										class="size-4 accent-accent"
									/>{FLAG_LABEL[f]}
								</label>
							{/each}
							{#each VALUE_FILTERS.filter((k) => valueChoices[k].length || filters[k]) as k (k)}
								<label class="flex flex-col gap-1">
									<span class="text-xs font-bold text-ink-muted">{VALUE_LABEL[k]}</span>
									<select
										class="h-8 rounded-md border border-line-strong bg-panel px-2 text-[13px] text-ink"
										value={filters[k] ?? ''}
										onchange={(e) =>
											setFilters({ ...filters, [k]: e.currentTarget.value || undefined })}
									>
										<option value="">Any</option>
										{#each valueChoices[k] as [v, n] (v)}
											<option value={v}>{valueText(k, v)} ({num(n)})</option>
										{/each}
									</select>
								</label>
							{/each}
							<Button variant="ghost" size="sm" class="self-end" onclick={() => (adding = false)}
								>Done</Button
							>
						</div>
					{/if}
				</span>
				{#if hasFilters(filters)}
					<button
						class="text-[13px] text-accent-ink hover:underline"
						onclick={() => setFilters({ flags: [] })}>Clear filters</button
					>
				{/if}
			</div>
			<span class="text-[13px] text-ink-soft"
				><strong>{num(shown.length)}</strong> of {num(total)}
				{filters.flags.includes('nodata')
					? total === 1
						? 'VM'
						: 'VMs'
					: total === 1
						? 'disk'
						: 'disks'}</span
			>
		</div>

		{#if selected.size > 0}
			<div class="flex flex-col gap-3 border-b border-line bg-select-soft px-card py-3.5">
				<div class="flex flex-wrap items-start gap-x-4 gap-y-2 text-[13px]">
					<span class="leading-8"
						>{#if selected.size === onPage}<strong>{num(onPage)} selected</strong> on this page{:else}<strong
								>{num(selected.size)} selected</strong
							>{/if}</span
					>
					{#if selected.size < shown.length}
						<button
							class="leading-8 text-accent-ink hover:underline"
							onclick={() => (selected = new Set(shown.map((r) => r.key)))}
							>Select all {num(shown.length)}</button
						>
					{/if}
					<button
						class="leading-8 text-accent-ink hover:underline"
						onclick={() => (selected = new Set())}>Clear selection</button
					>
					<div class="ml-auto flex flex-col items-end gap-1">
						<div class="flex items-center gap-2">
							{#if mode === 'move'}
								<StageChip kind="pr" />
								<Button
									disabled={bulkOp.busy || !move.edits.length || !target}
									title={moveWhy || (!target ? 'No shared class to move to' : '')}
									onclick={stageMove}
								>
									<ArrowLeftRight size={16} />Move {num(move.moving)}
									{move.moving === 1 ? 'disk' : 'disks'} to {target || 'a shared class'}
								</Button>
							{:else}
								<StageChip kind="now" />
								<Button
									disabled={bulkOp.busy || !snap.vms.length}
									title={snapWhy}
									onclick={snapshotNow}
								>
									<Camera size={16} />Snapshot {num(snap.vms.length)}
									{snap.vms.length === 1 ? 'VM' : 'VMs'} now
								</Button>
							{/if}
						</div>
						<span class="text-xs leading-4 text-ink-faint">
							{#if mode === 'move'}
								{moveWhy || moveNote(move) || 'Nothing here can move.'}
							{:else}
								{snapWhy || 'One snapshot per VM covers its disks. No pull request.'}
							{/if}
						</span>
					</div>
				</div>
				<div
					class="grid grid-cols-[repeat(auto-fit,minmax(min(280px,100%),1fr))] gap-x-5 gap-y-1.5"
				>
					{#if mode === 'move'}
						{#each moveConsequences(move) as c (c.text)}
							<Consequence tone={c.tone}>{c.text}</Consequence>
						{/each}
					{:else}
						<Consequence tone="info"
							>Running VMs keep running while the snapshot is taken.</Consequence
						>
						{#if snap.unsupported.length}
							<Consequence tone="warn"
								>{num(snap.unsupported.length)}
								{snap.unsupported.length === 1 ? 'disk is' : 'disks are'} on a class without snapshots
								and
								{snap.unsupported.length === 1 ? 'is' : 'are'} left out.</Consequence
							>
						{/if}
					{/if}
				</div>
			</div>
		{/if}

		<div class="max-h-[520px] overflow-auto">
			<table class="w-full border-collapse text-[13px] tabular-nums">
				<thead>
					<tr>
						{#if selectable}
							<th scope="col" class="{TH} w-[34px] pl-4">
								<input
									type="checkbox"
									class="size-4 align-middle accent-accent"
									checked={allOnPage}
									indeterminate={onPage > 0 && !allOnPage}
									aria-label="Select all {pageKeys.length} on this page"
									onchange={togglePage}
								/>
							</th>
						{/if}
						<th scope="col" class={TH} aria-sort={desc ? 'descending' : 'ascending'}>
							<button
								class="inline-flex items-center gap-0.5 font-bold"
								onclick={() => go({ sort: desc ? undefined : 'vm-desc' }, { replace: true })}
								>VM{#if desc}<ChevronDown size={12} />{:else}<ChevronUp size={12} />{/if}</button
							>
						</th>
						<th scope="col" class={TH}>Disk</th>
						<th scope="col" class="{TH} text-right">Size</th>
						<th scope="col" class={TH}>Class</th>
						<th scope="col" class={TH}>Host</th>
						<th scope="col" class={TH}>From image</th>
						<th scope="col" class={TH}>Last snapshot</th>
					</tr>
				</thead>
				<tbody>
					{#each pageRows as r (r.key)}
						{@const on = selected.has(r.key)}
						{@const current = r.key === openKey}
						{@const staged = drafts.stagedByKey
							.get(r.vmKey)
							?.edit?.migrateVolumes?.find((m) => m.name === r.disk.name)}
						<tr
							class="cursor-pointer {current || on ? 'bg-select' : 'hover:bg-select-soft'} {current
								? 'shadow-[inset_3px_0_0_var(--color-accent)]'
								: ''}"
							aria-current={current ? 'true' : undefined}
							onclick={() => go({ disk: current ? undefined : r.key }, { replace: true })}
						>
							{#if selectable}
								<td class="h-10 w-[34px] border-b border-line-soft pr-2 pl-4">
									<input
										type="checkbox"
										class="size-4 align-middle accent-accent"
										checked={on}
										aria-label="Select {r.vm.name} {r.disk.name}"
										onclick={(e) => e.stopPropagation()}
										onchange={() => toggle(r.key)}
									/>
								</td>
							{/if}
							<td class="h-10 border-b border-line-soft px-2 whitespace-nowrap">
								<a
									href={vmHref(r.vm.namespace, r.vm.name)}
									class="font-mono text-xs font-medium text-ink hover:text-accent-ink"
									onclick={(e) => e.stopPropagation()}>{r.vm.name}</a
								>{#if repeated.has(r.vm.name)}<span class="ml-1.5 text-xs text-ink-faint"
										>{r.vm.namespace}</span
									>{/if}{#if !r.running}<span class="ml-1.5 text-xs text-ink-faint">stopped</span
									>{/if}
							</td>
							<td class="h-10 border-b border-line-soft px-2 whitespace-nowrap">
								<!-- The row opens the disk on click; this is the keyboard way in. -->
								<button
									class="font-mono text-xs font-medium hover:text-accent-ink"
									aria-expanded={current}
									aria-label="Open {r.vm.name} {r.disk.name || r.disk.type}"
									onclick={(e) => {
										e.stopPropagation();
										go({ disk: current ? undefined : r.key }, { replace: true });
									}}>{r.disk.name || r.disk.type}</button
								>
								{#if staged}<span class="ml-1.5 align-[1px]"><StageChip kind="staged" /></span>{/if}
							</td>
							<td class="h-10 border-b border-line-soft px-2 text-right whitespace-nowrap">
								{r.disk.size ? quantity(r.disk.size) : '-'}
							</td>
							<td class="h-10 border-b border-line-soft px-2 whitespace-nowrap">
								{#if r.ephemeral}
									<span class="text-ink-faint"
										>None, {r.disk.type === 'containerDisk'
											? 'container image'
											: (r.disk.type ?? 'ephemeral')}</span
									>
								{:else}
									<span class="font-mono text-xs font-medium">{r.storageClass || '-'}</span>
								{/if}
							</td>
							<td class="h-10 border-b border-line-soft px-2 whitespace-nowrap">
								{#if r.host}
									<span class="font-mono text-xs font-medium">{r.host}</span>
									{#if r.maintenance}<span class="block text-[11px] leading-[14px] text-warn-ink"
											>in maintenance</span
										>{/if}
								{:else}
									<span class="text-ink-faint">-</span>
								{/if}
							</td>
							<td class="h-10 border-b border-line-soft px-2 whitespace-nowrap">
								{#if r.image}<span class="font-mono text-xs font-medium">{shortName(r.image)}</span>
								{:else if r.disk.import}
									{@const imp = r.disk.import}
									<span class="text-xs text-ink-faint">{SOURCE_WORD[imp.kind] ?? imp.kind}</span>
									<span
										class="inline-block max-w-[180px] truncate align-bottom font-mono text-xs font-medium"
										title={imp.source}>{sourceTail(imp)}</span
									>
								{:else}<span class="text-ink-faint">-</span>{/if}
							</td>
							<td class="h-10 border-b border-line-soft px-2 whitespace-nowrap">
								{#if r.ephemeral}
									<span class="text-ink-faint">Nothing to keep</span>
								{:else if storage.snapshots === null}
									<span class="text-ink-faint">-</span>
								{:else if !lastSnap(r)}
									<span class="text-warn-ink">None</span>
								{:else if !r.fresh}
									<span class="text-warn-ink"
										>{ago(lastSnap(r))}<span class="sr-only">, older than 7 days</span></span
									>
								{:else}
									{ago(lastSnap(r))}
								{/if}
							</td>
						</tr>
					{:else}
						<tr>
							<td colspan="8" class="px-card py-6 text-center text-[13px] text-ink-faint">
								{rows.length ? 'No disk matches these filters.' : 'No VM disks yet.'}
								{#if hasFilters(filters)}
									<button
										class="ml-1 text-accent-ink hover:underline"
										onclick={() => setFilters({ flags: [] })}>Clear filters</button
									>
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
			bind:page={
				() => s.page, (n) => go({ page: n > 1 ? String(n) : undefined }, { replace: true })
			}
			bind:size={
				() => size,
				(n) => go({ per: n === DEFAULT_PAGE_SIZE ? undefined : String(n) }, { replace: true })
			}
		/>
	</section>

	{#if open}
		<DiskPanel row={open} onclose={() => go({ disk: undefined }, { replace: true })} />
	{/if}
</div>
