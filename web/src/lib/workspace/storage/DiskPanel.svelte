<script lang="ts">
	import {
		ArrowLeftRight,
		Camera,
		Check,
		ChevronRight,
		MapPin,
		RotateCcw,
		X,
	} from '@lucide/svelte';
	import { api } from '#lib/api.js';
	import { quantity } from '#lib/format.js';
	import { hrefForScope, storageHref } from '#lib/nav.js';
	import { itemKey } from '#lib/review.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import {
		ago,
		classFacts,
		num,
		planBulkMove,
		shortName,
		suggestTarget,
		type DiskRow,
	} from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import StageChip from '#lib/components/StageChip.svelte';

	// One disk beside the table: where it came from, what its class allows
	// against where it could go, the host it is pinned to, its last snapshots,
	// and the three things to do about it, each with how it lands.
	let { row, onclose }: { row: DiskRow; onclose: () => void } = $props();

	const vm = $derived(row.vm);
	const classes = $derived(storage.classes ?? []);
	const info = (name: string) => classes.find((c) => c.name === name);
	const stagedItem = $derived(drafts.stagedByKey.get(row.vmKey));
	const stagedTo = $derived(
		stagedItem?.edit?.migrateVolumes?.find((m) => m.name === row.disk.name)?.storageClass ?? '',
	);
	const target = $derived(stagedTo || (row.shared === false ? suggestTarget(classes) : ''));
	const here = $derived(classFacts(info(row.storageClass)));
	const there = $derived(target ? classFacts(info(target)) : null);
	const plan = $derived(
		target && !stagedTo
			? planBulkMove([row], target, { classes, staged: drafts.stagedByKey, rows: storage.rows })
			: null,
	);

	const runState = $derived(row.running ? `${vm.name} is running.` : `${vm.name} is stopped.`);
	const size = $derived(
		row.disk.size ? `${quantity(row.disk.size)} in ${row.project}.` : `In ${row.project}.`,
	);

	// The restore point: the newest snapshot until another is picked.
	let picked = $state('');
	$effect(() => {
		void row.key;
		picked = '';
	});
	const last = $derived(row.snapshots.slice(0, 3));
	const point = $derived(row.snapshots.find((s) => s.name === picked) ?? row.snapshots[0]);
	const when = (iso?: string) => {
		const a = ago(iso);
		return a === '1 day ago' ? 'Yesterday' : a;
	};

	const op = action({ toast: ui.toastError });
	let armed = $state(false);
	$effect(() => {
		void row.key;
		void point?.name;
		armed = false;
	});

	function stageMove() {
		const e = plan?.edits[0];
		if (!e) return;
		op.run(async () => {
			await api.stageEdit(vm.namespace, vm.name, { sourceFile: vm.sourceFile, ...e.edit });
			ui.toastStaged(`Move of ${vm.name} ${row.disk.name} to ${target} staged.`);
		});
	}
	function snapshotNow() {
		op.run(async () => {
			const { name } = await api.takeSnapshot(vm.namespace, vm.name);
			ui.showToast(`Snapshot ${name} of ${vm.name} started.`, { kind: 'success' });
			await storage.refreshSnapshots();
		});
	}
	function restore() {
		if (!point) return;
		if (!armed) {
			armed = true;
			return;
		}
		op.run(async () => {
			await api.restoreSnapshot(vm.namespace, vm.name, point.name);
			armed = false;
			ui.showToast(`Restoring ${vm.name} to ${point.name}.`, { kind: 'success' });
		});
	}
	function stagePowerOff() {
		op.run(async () => {
			await api.stageEdit(vm.namespace, vm.name, {
				...(stagedItem?.edit as object | undefined),
				sourceFile: vm.sourceFile,
				power: 'Off',
			});
			ui.toastStaged(`Power off of ${vm.name} staged. Restore once it merges.`);
		});
	}
	const snapshotsAllowed = $derived(here.snapshots !== false);
</script>

{#snippet verdict(v: boolean | null, yes: string, no: string)}
	{#if v === null}
		<span class="text-ink-faint">Unknown</span>
	{:else if v}
		<span class="inline-flex items-center gap-1.5 text-ok-ink"
			><Check size={14} strokeWidth={2.2} class="text-ok" />{yes}</span
		>
	{:else}
		<span class="inline-flex items-center gap-1.5 text-danger-ink"
			><X size={14} strokeWidth={2.2} class="text-danger" />{no}</span
		>
	{/if}
{/snippet}

<aside
	aria-labelledby="disk-panel-h"
	class="flex min-w-0 flex-[1_1_360px] flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
>
	<div
		class="flex items-start gap-2 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
	>
		<div class="min-w-0 flex-1">
			<h2 id="disk-panel-h" class="font-mono text-base leading-6 font-medium">
				{vm.name}
				{row.disk.name}
			</h2>
			<span class="block text-[13px] text-ink-muted">{size} {runState}</span>
		</div>
		<button
			class="flex size-[30px] shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong"
			aria-label="Close {vm.name} {row.disk.name}"
			onclick={onclose}><X size={16} /></button
		>
	</div>

	<div class="flex flex-col gap-2 px-card py-3.5">
		<div class="flex flex-wrap items-center gap-1.5" role="group" aria-label="Lineage">
			{#if row.image}
				<a
					href={storageHref('images', { image: row.image })}
					class="inline-flex h-7 items-center rounded-md border border-line bg-panel px-2.5 font-mono text-[13px] font-medium text-accent-ink hover:border-accent"
					>{shortName(row.image)}</a
				>
			{:else}
				<span
					class="inline-flex h-7 items-center rounded-md border border-line bg-panel px-2.5 text-[13px] text-ink-faint"
					>No image</span
				>
			{/if}
			<ChevronRight size={14} class="text-ink-faint" />
			<span
				aria-current="true"
				class="inline-flex h-7 items-center rounded-md border border-accent bg-select px-2.5 font-mono text-[13px] font-medium"
				>{row.disk.name}</span
			>
			<ChevronRight size={14} class="text-ink-faint" />
			<a
				href={storageHref('snapshots', { vm: row.vmKey })}
				class="inline-flex h-7 items-center rounded-md border border-line bg-panel px-2.5 text-[13px] text-accent-ink hover:border-accent"
				>{num(row.snapshots.length)} {row.snapshots.length === 1 ? 'snapshot' : 'snapshots'}</a
			>
		</div>
	</div>

	<div class="flex flex-col gap-2 border-t border-line-soft px-card py-3.5">
		<h3 class="text-[13px] leading-5 font-bold">What the class allows</h3>
		<table class="w-full border-collapse text-[13px]">
			<thead>
				<tr class="text-left text-xs font-bold text-ink-muted">
					<th scope="col" class="w-[38%] pb-1.5"><span class="sr-only">Property</span></th>
					<th scope="col" class="pb-1.5 font-mono font-medium text-ink"
						>{row.storageClass || 'Unknown'}</th
					>
					{#if there}
						<th scope="col" class="pb-1.5"
							><span class="font-mono font-medium text-ink">{target}</span>{stagedTo
								? ', staged'
								: ''}</th
						>
					{/if}
				</tr>
			</thead>
			<tbody
				class="[&_td]:border-t [&_td]:border-line-soft [&_td]:py-[5px] [&_th]:border-t [&_th]:border-line-soft [&_th]:py-[5px] [&_th]:text-left [&_th]:font-normal [&_th]:text-ink-muted"
			>
				<tr>
					<th scope="row">Live migration</th>
					<td>{@render verdict(row.shared, 'Possible', 'Not possible')}</td>
					{#if there}<td>{@render verdict(there.liveMigration, 'Possible', 'Not possible')}</td
						>{/if}
				</tr>
				<tr>
					<th scope="row">Shared across hosts</th>
					<td>{@render verdict(row.shared, 'Yes', 'No')}</td>
					{#if there}<td>{@render verdict(there.shared, 'Yes', 'No')}</td>{/if}
				</tr>
				<tr>
					<th scope="row">Snapshots</th>
					<td>{@render verdict(here.snapshots, 'Yes', 'No')}</td>
					{#if there}<td>{@render verdict(there.snapshots, 'Yes', 'No')}</td>{/if}
				</tr>
				<tr>
					<th scope="row">Free space</th>
					<td>{here.free}</td>
					{#if there}<td>{there.free}</td>{/if}
				</tr>
			</tbody>
		</table>
		{#if row.shared === false && row.host}
			<div class="flex items-start gap-2.5 pt-1 text-[13px] text-ink-soft">
				<MapPin size={16} class="mt-0.5 shrink-0 text-ink-muted" />
				<div>
					<strong>Pinned to {row.host}.</strong>
					{#if row.running}
						{vm.name} runs only there, and stops when {row.host} goes into maintenance.
					{:else}
						{vm.name} can only start there{row.maintenance
							? `, and ${row.host} is in maintenance`
							: ''}.
					{/if}
					<a
						href={hrefForScope({ kind: 'node', node: row.host })}
						class="text-accent-ink hover:underline">Open {row.host}</a
					>
				</div>
			</div>
		{/if}
	</div>

	<div class="flex flex-col gap-2 border-t border-line-soft px-card py-3.5">
		<div class="flex flex-wrap items-baseline justify-between gap-2">
			<h3 class="text-[13px] leading-5 font-bold">
				{row.snapshots.length > 3 ? 'Last 3 snapshots' : 'Snapshots'}
			</h3>
			{#if row.snapshots.length}
				<a
					href={storageHref('snapshots', { vm: row.vmKey })}
					class="text-[13px] text-accent-ink hover:underline"
					>All {num(row.snapshots.length)} on the Snapshots tab</a
				>
			{/if}
		</div>
		{#if last.length}
			<ol
				class="relative flex flex-col before:absolute before:top-3.5 before:bottom-3.5 before:left-[7px] before:border-l-2 before:border-line"
			>
				{#each last as s (s.name)}
					<li>
						<label class="relative flex h-[30px] cursor-pointer items-center gap-2.5 text-[13px]">
							<input
								type="radio"
								name="restore-point"
								class="size-4 accent-accent"
								checked={point?.name === s.name}
								onchange={() => (picked = s.name)}
							/>
							<span class="min-w-20 font-medium">{when(s.created)}</span>
							<span class="truncate font-mono text-xs text-ink-muted">{s.name}</span>
						</label>
					</li>
				{/each}
			</ol>
		{:else if storage.snapshots === null}
			<p class="text-[13px] text-ink-faint">Reading snapshots.</p>
		{:else}
			<p class="text-[13px] text-ink-faint">No snapshot of this disk yet.</p>
		{/if}
	</div>

	<div class="flex flex-col border-t border-line-soft px-card py-3.5">
		<div class="flex flex-col gap-1.5">
			<div class="flex flex-wrap items-center gap-2">
				<Button
					variant="outline"
					size="sm"
					disabled={op.busy || !plan?.edits.length}
					title={plan?.blocked[0]?.reason ?? ''}
					onclick={stageMove}><ArrowLeftRight size={14} />Move disk</Button
				>
				<StageChip kind="pr" />
			</div>
			{#if stagedTo}
				<p class="text-xs leading-4 text-ink-faint">
					<strong class="text-ink-soft">Staged to {stagedTo}</strong> in the {row.project} draft.
					<button
						class="text-accent-ink hover:underline"
						onclick={() =>
							ui.openChanges({
								kind: 'item',
								project: row.project,
								key: itemKey({ namespace: vm.namespace, name: vm.name }),
							})}>Review</button
					>
				</p>
				<p class="text-xs leading-4 text-ink-faint">
					Copies live while {vm.name} runs. Once merged, {vm.name} can live-migrate.
				</p>
			{:else if !target}
				<p class="text-xs leading-4 text-ink-faint">
					{row.shared
						? `On a shared class already: ${vm.name} can live-migrate with it.`
						: 'No shared class to move it to.'}
				</p>
			{:else if plan?.stopped.length}
				<Consequence tone="danger" size="sm"
					>{vm.name} is stopped, and a disk copies only while its VM runs. Start it first.</Consequence
				>
			{:else if plan?.blocked.length}
				<Consequence tone="warn" size="sm"
					>Cannot move it: {vm.name} {row.disk.name} is {plan.blocked[0].reason}.</Consequence
				>
			{:else}
				<p class="text-xs leading-4 text-ink-faint">
					Stages a move to {target} in the {row.project} draft. Copies live while {vm.name} runs{plan?.liveMigrateAfter
						? `. Once merged, ${vm.name} can live-migrate.`
						: '.'}
				</p>
			{/if}
		</div>

		<div class="mt-3 flex flex-col gap-1.5 border-t border-line-soft pt-3">
			<div class="flex flex-wrap items-center gap-2">
				<Button
					variant="secondary"
					size="sm"
					disabled={op.busy || !snapshotsAllowed}
					onclick={snapshotNow}><Camera size={14} />Snapshot now</Button
				>
				<StageChip kind="now" />
			</div>
			<p class="text-xs leading-4 text-ink-faint">
				{#if !snapshotsAllowed}
					{row.storageClass} cannot take snapshots.
				{:else}
					Snapshots {vm.name} now{row.running ? ', while it runs' : ''}. No pull request.
				{/if}
			</p>
		</div>

		{#if point}
			<div class="mt-3 flex flex-col gap-1.5 border-t border-line-soft pt-3">
				<div class="flex flex-wrap items-center gap-2">
					<Button
						variant={armed ? 'danger' : 'secondary'}
						size="sm"
						disabled={op.busy || row.running}
						aria-label="Restore to {point.name}"
						onclick={restore}><RotateCcw size={14} />{armed ? 'Confirm restore' : 'Restore'}</Button
					>
					<StageChip kind="now" />
				</div>
				<div class="flex flex-col gap-1">
					<Consequence tone="danger" size="sm"
						>Puts {vm.name} back to {point.name}, every disk it captured. Writes since then are
						lost.</Consequence
					>
					{#if row.running}
						<Consequence tone="warn" size="sm">
							{vm.name} must stop first. Power off stages a pull request; restore once it merges.
							{#if vm.sourceFile && stagedItem?.edit?.power !== 'Off'}
								<button
									class="text-accent-ink hover:underline"
									disabled={op.busy}
									onclick={stagePowerOff}>Stage power off</button
								>
							{:else if stagedItem?.edit?.power === 'Off'}
								Power off is staged.
							{/if}
						</Consequence>
					{/if}
				</div>
			</div>
		{/if}
	</div>
</aside>
