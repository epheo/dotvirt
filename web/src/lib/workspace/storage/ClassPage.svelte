<script lang="ts">
	import { hrefForScope, storageHref, trailForScope } from '#lib/nav.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { FRESH_DAYS, ago, classFacts, classTag, num, size } from '#lib/storage.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';

	// One class as a drill-in: what a disk on it gets, where its free space
	// is, and its disks as counts that open the Disks tab filtered to it.
	let { name }: { name: string } = $props();

	storage.ensure();

	const SEGMENTS = 8;
	const info = $derived(storage.classes?.find((c) => c.name === name));
	const missing = $derived(storage.classes !== null && !info);
	const facts = $derived(classFacts(info));
	const disks = $derived(storage.rows.filter((r) => !r.ephemeral && r.storageClass === name));
	const vms = $derived(new Set(disks.map((d) => d.vmKey)).size);
	const held = $derived(disks.reduce((n, d) => n + (Number.isNaN(d.size) ? 0 : d.size), 0));
	const counts = $derived([
		{ label: 'cannot live-migrate', n: disks.filter((d) => d.shared === false).length, f: 'nolm' },
		{
			label: `without a snapshot in ${FRESH_DAYS} days`,
			n: storage.snapshots === null ? null : disks.filter((d) => !d.fresh).length,
			f: 'nosnap',
		},
		{ label: 'belong to stopped VMs', n: disks.filter((d) => !d.running).length, f: 'stopped' },
	]);

	// The fullest hosts first: that is where the next local disk will not fit.
	let allSegments = $state(false);
	const segments = $derived([...(info?.segments ?? [])].sort((a, b) => a.free - b.free));
	const shownSegments = $derived(allSegments ? segments : segments.slice(0, SEGMENTS));

	const status = $derived(
		missing
			? { tone: 'danger' as const, text: 'Not on the cluster' }
			: info?.profile && !info.profile.recognized
				? { tone: 'warn' as const, text: 'Unrecognized by CDI' }
				: info
					? { tone: 'ok' as const, text: 'Available' }
					: undefined,
	);
	const context = $derived(
		[info ? classTag(info) : '', info?.description ?? ''].filter(Boolean).join('. '),
	);

	const verdict = (v: boolean | null, yes: string, no: string) =>
		v === null ? 'Unknown' : v ? yes : no;
	const ROW =
		'grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 border-t border-line-soft py-2 first:border-t-0';
</script>

<div class="min-h-0 flex-1 overflow-y-auto bg-canvas">
	<div class="flex min-h-full flex-col gap-[18px] px-7 pt-5 pb-8">
		<div class="flex flex-col gap-2">
			<Breadcrumb trail={trailForScope({ kind: 'storage', storageClass: name })} />
			<PageHeader title={name} {status} {context}>
				{#snippet actions()}
					{#if disks.length}
						<Button variant="secondary" href={storageHref('disks', { class: name })}
							>Show the {num(disks.length)} {disks.length === 1 ? 'disk' : 'disks'}</Button
						>
					{/if}
				{/snippet}
			</PageHeader>
		</div>

		{#if missing}
			<p
				class="rounded-lg border border-danger/30 bg-danger-soft px-4 py-3 text-[13px] text-danger-ink"
			>
				This class does not exist on the cluster, so disks on it cannot be provisioned. Move them to
				an existing class from the Disks tab.
			</p>
		{/if}

		<div class="grid grid-cols-[repeat(auto-fit,minmax(min(360px,100%),1fr))] items-start gap-4">
			<Card title="What a disk here gets" meta="Managed by the cluster platform, not dotvirt">
				<dl class="text-[13px]">
					<div class={ROW}>
						<dt class="text-ink-muted">Live migration</dt>
						<dd>
							{verdict(facts.liveMigration, 'Possible', 'Not possible: the disk stays on one host')}
						</dd>
					</div>
					<div class={ROW}>
						<dt class="text-ink-muted">Snapshots</dt>
						<dd>{info ? info.snapshotClass || 'Not available' : 'Unknown'}</dd>
					</div>
					<div class={ROW}>
						<dt class="text-ink-muted">Disk resize</dt>
						<dd>{info ? (info.expandable ? 'Allowed' : 'Not allowed') : 'Unknown'}</dd>
					</div>
					{#if info?.profile}
						<div class={ROW}>
							<dt class="text-ink-muted">Access modes</dt>
							<dd>{info.profile.accessModes.join(', ') || '-'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Volume mode</dt>
							<dd>{info.profile.volumeMode || '-'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Clone strategy</dt>
							<dd>{info.profile.cloneStrategy || '-'}</dd>
						</div>
					{/if}
					{#if info}
						<div class={ROW}>
							<dt class="text-ink-muted">Volume binding</dt>
							<dd>{info.bindingMode || '-'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Reclaim policy</dt>
							<dd>{info.reclaimPolicy || '-'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Provisioner</dt>
							<dd class="font-mono text-xs">{info.provisioner}</dd>
						</div>
						{#if info.created}
							<div class={ROW}>
								<dt class="text-ink-muted">Created</dt>
								<dd>{ago(info.created)}</dd>
							</div>
						{/if}
					{/if}
				</dl>
			</Card>

			<div class="flex flex-col gap-4">
				<Card
					title="Disks"
					meta={disks.length
						? `${num(disks.length)} on ${num(vms)} ${vms === 1 ? 'VM' : 'VMs'}, ${size(held)}`
						: ''}
				>
					{#if disks.length}
						<ul class="flex flex-col gap-1.5 text-[13px]">
							{#each counts as c (c.f)}
								<li class="flex items-baseline gap-2">
									{#if c.n === null}
										<span class="text-ink-faint">Reading snapshots</span>
									{:else if c.n === 0}
										<span class="text-ink-faint">None {c.label}</span>
									{:else}
										<a
											class="text-accent-ink hover:underline"
											href={storageHref('disks', { class: name, f: c.f })}>{num(c.n)} {c.label}</a
										>
									{/if}
								</li>
							{/each}
						</ul>
					{:else}
						<p class="text-[13px] text-ink-faint">No VM disk is on this class.</p>
					{/if}
				</Card>

				<Card title="Free space" meta={facts.free}>
					{#if segments.length > 1}
						<ul class="flex flex-col text-[13px]">
							{#each shownSegments as seg (seg.topology)}
								<li
									class="flex justify-between gap-3 border-t border-line-soft py-1.5 first:border-t-0"
								>
									{#if seg.topology}
										<a
											class="text-accent-ink hover:underline"
											href={hrefForScope({ kind: 'node', node: seg.topology })}
											>Free on {seg.topology}</a
										>
									{:else}<span>Free cluster-wide</span>{/if}
									<span>{size(seg.free)}</span>
								</li>
							{/each}
						</ul>
						{#if segments.length > SEGMENTS}
							<button
								class="mt-2 text-[13px] text-accent-ink hover:underline"
								onclick={() => (allSegments = !allSegments)}
								>{allSegments
									? 'Fewer hosts'
									: `${num(segments.length - SEGMENTS)} more hosts`}</button
							>
						{/if}
					{:else if info?.free == null}
						<p class="text-[13px] text-ink-faint">
							The storage driver does not report free space for this class.
						</p>
					{:else if facts.shared === false}
						<!-- The per-host split names hosts, so it reaches only callers who
						     may read nodes; the total is all anyone else gets. -->
						<p class="text-[13px]">
							{size(info.free)} free in total, spread across hosts. A disk here takes its space on one
							host.
						</p>
						{#if segments.length === 0}
							<p class="mt-1 text-xs text-ink-faint">
								The split per host shows to accounts that can see hosts.
							</p>
						{/if}
					{:else}
						<p class="text-[13px]">{size(info.free)} free across the cluster.</p>
					{/if}
				</Card>
			</div>
		</div>
	</div>
</div>
