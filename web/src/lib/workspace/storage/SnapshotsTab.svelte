<script lang="ts">
	import { page } from '$app/state';
	import { Funnel, X } from '@lucide/svelte';
	import { api, type Snapshot } from '#lib/api.js';
	import { vmHref } from '#lib/nav.js';
	import { action } from '#lib/resource.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { ago, num, repeatedNames } from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';

	// Every VM snapshot the caller can read, newest first, paged. A VM filter
	// arrives from a disk's lineage; failures float up on their own filter.
	let {
		snapshots,
		loaded,
		go,
	}: {
		snapshots: Snapshot[];
		loaded: boolean;
		go: (params: Record<string, string | undefined>, opts?: { replace?: boolean }) => void;
	} = $props();

	const q = $derived(page.url.searchParams);
	const vmFilter = $derived(q.get('vm') ?? '');
	const failedOnly = $derived(q.get('state') === 'failed');
	const failed = (s: Snapshot) => s.phase === 'Failed' || !!s.error;
	const shown = $derived(
		snapshots.filter(
			(s) => (!vmFilter || `${s.namespace}/${s.vm}` === vmFilter) && (!failedOnly || failed(s)),
		),
	);
	const failures = $derived(snapshots.filter(failed).length);
	const repeated = $derived(
		repeatedNames(
			shown,
			(s) => s.namespace ?? '',
			(s) => s.vm ?? '',
		),
	);
	const size = $derived(Number(q.get('per')) || DEFAULT_PAGE_SIZE);
	const s = $derived(pageSlice(shown.length, Number(q.get('page')) || 1, size));
	const pageRows = $derived(shown.slice(s.start, s.end));
	const projectOf = $derived(
		new Map(
			inventory.projects.flatMap((p) => p.namespaces.map((n) => [n.namespace, p.name] as const)),
		),
	);

	const op = action({ toast: ui.toastError });
	let armed = $state('');
	function remove(s: Snapshot) {
		const key = `${s.namespace}/${s.name}`;
		if (armed !== key) {
			armed = key;
			return;
		}
		op.run(async () => {
			await api.deleteSnapshot(s.namespace!, s.vm!, s.name);
			armed = '';
			ui.showToast(`Snapshot ${s.name} deleted.`, { kind: 'success' });
			await storage.refreshSnapshots();
		});
	}

	const TH =
		'border-b border-line px-2 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'h-10 border-b border-line-soft px-2 whitespace-nowrap';
	const CHIP =
		'inline-flex h-7 items-center gap-1.5 rounded-md border border-accent bg-panel pr-1 pl-2.5 text-[13px] text-ink';
</script>

<section
	aria-labelledby="snaps-h"
	class="flex min-w-0 flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
>
	<h2 id="snaps-h" class="sr-only">Snapshots</h2>
	<div
		class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
	>
		<div class="flex flex-wrap items-center gap-2">
			<Funnel size={16} class="text-ink-muted" />
			{#if vmFilter}
				<span class={CHIP}
					><span class="text-ink-muted">VM</span>{vmFilter.split('/')[1]}<button
						class="flex size-[22px] items-center justify-center rounded text-ink-muted hover:bg-inset"
						aria-label="Remove filter: VM {vmFilter}"
						onclick={() => go({ vm: undefined, page: undefined }, { replace: true })}
						><X size={14} /></button
					></span
				>
			{/if}
			<Button
				variant={failedOnly ? 'outline' : 'secondary'}
				size="sm"
				aria-pressed={failedOnly}
				disabled={!failures && !failedOnly}
				onclick={() =>
					go({ state: failedOnly ? undefined : 'failed', page: undefined }, { replace: true })}
				>Failed {num(failures)}</Button
			>
		</div>
		<span class="text-[13px] text-ink-soft"
			><strong>{num(shown.length)}</strong> of {num(snapshots.length)} snapshots</span
		>
	</div>

	<div class="overflow-x-auto">
		<table class="w-full border-collapse text-[13px] tabular-nums">
			<thead>
				<tr>
					<th scope="col" class="{TH} pl-card">VM</th>
					<th scope="col" class={TH}>Snapshot</th>
					<th scope="col" class={TH}>Taken</th>
					<th scope="col" class={TH}>State</th>
					<th scope="col" class={TH}>Disks</th>
					<th scope="col" class={TH}>Project</th>
					<th scope="col" class="{TH} pr-card"><span class="sr-only">Actions</span></th>
				</tr>
			</thead>
			<tbody>
				{#each pageRows as s (`${s.namespace}/${s.name}`)}
					{@const key = `${s.namespace}/${s.name}`}
					<tr class="hover:bg-select-soft">
						<td class="{TD} pl-card">
							<a
								href={vmHref(s.namespace ?? '', s.vm ?? '')}
								class="font-mono text-xs font-medium text-ink hover:text-accent-ink">{s.vm}</a
							>{#if repeated.has(s.vm ?? '')}<span class="ml-1.5 text-xs text-ink-faint"
									>{s.namespace}</span
								>{/if}
						</td>
						<td class="{TD} font-mono text-xs">{s.name}</td>
						<td class={TD} title={s.created}>{ago(s.created)}</td>
						<td class={TD}>
							{#if failed(s)}
								<span class="text-danger-ink" title={s.error}>Failed</span>
							{:else if s.readyToUse}
								Ready
							{:else}
								<span class="text-ink-muted">In progress</span>
							{/if}
						</td>
						<td class="{TD} font-mono text-xs text-ink-muted">{s.volumes?.join(', ') || 'All'}</td>
						<td class={TD}>{projectOf.get(s.namespace ?? '') ?? ''}</td>
						<td class="{TD} pr-card text-right">
							<Button
								variant={armed === key ? 'danger' : 'danger-secondary'}
								size="sm"
								disabled={op.busy}
								aria-label="Delete snapshot {s.name}"
								onclick={() => remove(s)}>{armed === key ? 'Confirm delete' : 'Delete'}</Button
							>
						</td>
					</tr>
				{:else}
					<tr>
						<td colspan="7" class="px-card py-6 text-center text-ink-faint">
							{#if !loaded}
								{storage.snapshotsError || 'Reading snapshots.'}
								{#if storage.snapshotsError}
									<button
										class="ml-1 text-accent-ink hover:underline"
										onclick={() => storage.refreshSnapshots()}>Try again</button
									>
								{/if}
							{:else if snapshots.length}
								No snapshot matches these filters.
							{:else}
								No snapshots yet. Take one from a disk on the Disks tab.
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
		bind:page={() => s.page, (n) => go({ page: n > 1 ? String(n) : undefined }, { replace: true })}
		bind:size={
			() => size,
			(n) => go({ per: n === DEFAULT_PAGE_SIZE ? undefined : String(n) }, { replace: true })
		}
	/>
</section>
