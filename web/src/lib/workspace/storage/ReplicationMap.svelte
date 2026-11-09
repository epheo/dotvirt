<script lang="ts">
	import { page } from '$app/state';
	import type { ReplicatedVolume, Replication } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { hrefForScope, vmHref } from '#lib/nav.js';
	import {
		cellFor,
		diskIfDown,
		diskNow,
		hostsOf,
		mapRows,
		protectionOf,
		rehearse,
		rowIfDown,
		tierRooms,
		type RowFate,
	} from '#lib/replication.js';
	import { TONE_TEXT, type Tone } from '#lib/status.js';
	import { size, type DiskRow } from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import CopyGlyph from './CopyGlyph.svelte';
	import MoveCopiesModal from './MoveCopiesModal.svelte';
	import SplitBrainModal from './SplitBrainModal.svelte';

	// Where every copy lives, one column per host. A click on a host rehearses
	// losing it, read from placement alone: nothing changes on the cluster.
	let {
		overview,
		rows,
		project,
		namespaces,
		go,
	}: {
		overview: Replication;
		rows: DiskRow[];
		project: string;
		namespaces: Set<string>;
		go: (params: Record<string, string | undefined>, opts?: { replace?: boolean }) => void;
	} = $props();

	// By namespace: a claim no VM holds has no project of its own to compare.
	const mapped = $derived(
		mapRows(
			overview.volumes.filter((v) => !project || namespaces.has(v.namespace ?? '')),
			rows,
		),
	);
	const hosts = $derived(
		hostsOf(
			overview.volumes,
			overview.pools,
			overview.hosts.map((h) => h.name),
		),
	);
	const down = $derived.by(() => {
		const raw = page.url.searchParams.get('down') ?? '';
		return hosts.includes(raw) ? raw : '';
	});
	const rehearsal = $derived(down ? rehearse(mapped, down) : null);
	const repair = $derived(clusterGate.replicationRepair());
	let splitOpen = $state<{ volume: ReplicatedVolume; name: string } | null>(null);
	let moveOff = $state('');
	const nameOf = (volume: string) => {
		const row = mapped.find((r) => r.disks.some((d) => d.volume.name === volume));
		const disk = row?.disks.find((d) => d.volume.name === volume);
		return row && disk && row.vm ? `${row.name} ${disk.label}` : (row?.name ?? volume);
	};
	const protection = $derived(protectionOf(mapped));
	const rooms = $derived(tierRooms(overview.pools));
	const blank = $derived(overview.hosts.filter((h) => h.blank?.length));
	const noun = $derived(mapped.every((r) => r.vm) ? 'VM' : 'workload');

	function used(host: string): number {
		const pools = overview.pools.filter((p) => p.node === host);
		const total = pools.reduce((n, p) => n + p.totalBytes, 0);
		return total
			? Math.round((100 * (total - pools.reduce((n, p) => n + p.freeBytes, 0))) / total)
			: 0;
	}
	const toggle = (host: string) =>
		go({ down: host === down ? undefined : host }, { replace: true });

	const FATE_TONE: Record<RowFate, Tone> = {
		off: 'danger',
		weak: 'warn',
		restart: 'info',
		ok: 'neutral',
	};
	const tile = (fate: RowFate, one: string, many: string) => {
		const list = rehearsal?.byFate[fate] ?? [];
		return {
			tone: list.length ? FATE_TONE[fate] : ('neutral' as Tone),
			n: list.length,
			label: list.length === 1 ? one : many,
			names: list.map((r) => r.name).join(', ') || 'None',
		};
	};
	const tiles = $derived([
		tile('off', `${noun} goes offline`, `${noun}s go offline`),
		tile('weak', 'keeps running, less protected', 'keep running, less protected'),
		tile('restart', `${noun} restarts elsewhere`, `${noun}s restart elsewhere`),
		tile('ok', 'unaffected', 'unaffected'),
	]);
	const TILE: Record<Tone, string> = {
		danger: 'border-danger/30 bg-danger-soft text-danger-ink',
		warn: 'border-warn/50 bg-warn-soft text-warn-ink',
		info: 'border-accent/30 bg-accent-soft text-accent-ink',
		ok: 'border-line bg-panel text-ink-muted',
		neutral: 'border-line bg-panel text-ink-muted',
	};
	const SEGMENT = {
		safe: 'bg-glyph',
		thin: 'bg-glyph-2',
		local: 'bg-line-strong',
		trouble: 'bg-danger',
	};

	const TH = 'border-b border-line px-2 py-2 text-xs font-bold whitespace-nowrap text-ink-muted';
	const CELL = 'border-b border-line-soft px-1 py-1.5 text-center align-middle';
	const STICKY = 'sticky left-0 z-10 bg-panel text-left';
</script>

<div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
	{#if rehearsal}
		<section
			aria-label="Rehearsal of losing {rehearsal.host}"
			class="flex min-w-0 flex-col gap-3 rounded-lg border border-line bg-panel p-4"
		>
			<div class="flex flex-wrap items-center justify-between gap-2">
				<div>
					<p class="text-xs font-bold tracking-wider text-ink-faint uppercase">
						Rehearsal, nothing changes on the cluster
					</p>
					<h2 class="text-lg font-bold text-ink">If {rehearsal.host} goes down</h2>
				</div>
				<Button variant="secondary" size="sm" onclick={() => toggle(rehearsal.host)}>
					End rehearsal
				</Button>
			</div>
			<div class="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
				{#each tiles as t (t.label)}
					<div class="flex min-w-0 flex-col rounded-md border p-3 {TILE[t.tone]}">
						<span class="text-2xl leading-8 font-bold">{t.n}</span>
						<span class="text-[13px] font-medium">{t.label}</span>
						<span class="mt-1 truncate text-xs opacity-80" title={t.names}>{t.names}</span>
					</div>
				{/each}
			</div>
			<div class="grid gap-3 sm:grid-cols-2">
				<div class="flex flex-col gap-1.5 rounded-md bg-inset p-3">
					<h3 class="text-[13px] font-bold text-ink">It comes back</h3>
					<p class="text-[13px] text-ink-muted">{rehearsal.maintenance}</p>
					{#if clusterGate.hosts().ok}
						<Button
							variant="outline"
							size="sm"
							class="mt-1 self-start"
							href={hrefForScope({ kind: 'node', node: rehearsal.host })}
						>
							Open {rehearsal.host}
						</Button>
					{/if}
				</div>
				<div class="flex flex-col gap-1.5 rounded-md bg-inset p-3">
					<h3 class="text-[13px] font-bold text-ink">It never comes back</h3>
					<p class="text-[13px] text-ink-muted">{rehearsal.retire}</p>
					{#if repair.ok}
						<Button
							variant="outline"
							size="sm"
							class="mt-1 self-start"
							onclick={() => (moveOff = rehearsal.host)}
						>
							Move copies off {rehearsal.host}
						</Button>
					{/if}
				</div>
			</div>
		</section>
	{:else}
		<Card title="Protection" meta="Click a host to rehearse losing it">
			<div class="flex flex-col gap-3">
				<p class="text-lg text-ink">
					<strong>{protection.survive} of {protection.total}</strong>
					disks survive losing any one host
				</p>
				{#if protection.total}
					<div class="flex h-2.5 gap-px overflow-hidden rounded-sm" aria-hidden="true">
						{#each protection.segments as s, i (i)}
							<span class="min-w-0 flex-1 {SEGMENT[s]}"></span>
						{/each}
					</div>
				{/if}
				{#if protection.notes.length}
					<ul class="flex flex-col gap-1 text-[13px] text-ink-muted">
						{#each protection.notes as note (note)}<li>{note}</li>{/each}
					</ul>
				{/if}
			</div>
		</Card>
	{/if}

	<Card title="Room by tier">
		<div class="flex flex-col gap-3">
			{#each rooms as r (r.tier)}
				<div class="flex flex-col gap-1">
					<div class="flex items-baseline justify-between gap-2 text-[13px]">
						<span class="font-bold text-ink">{r.tier}</span>
						<span class="text-ink-faint">
							{size(r.usedBytes)} of {size(r.totalBytes)}, {r.pools}
							{r.pools === 1 ? 'pool' : 'pools'}
						</span>
					</div>
					<div class="h-1.5 overflow-hidden rounded-sm bg-inset-strong">
						<div
							class="h-full bg-glyph"
							style:width="{r.totalBytes ? (100 * r.usedBytes) / r.totalBytes : 0}%"
						></div>
					</div>
					<span class="text-xs text-ink-muted">
						{r.largest
							? `Largest new 2-copy disk today: ${size(r.largest)}`
							: 'No room for a 2-copy disk'}
					</span>
				</div>
			{:else}
				<p class="text-[13px] text-ink-muted">No pools yet.</p>
			{/each}
			{#each blank as h (h.name)}
				<p class="border-t border-line-soft pt-2 text-xs text-ink-muted">
					{h.name} has {h.blank?.length} blank {h.blank?.length === 1 ? 'disk' : 'disks'}.
				</p>
			{/each}
		</div>
	</Card>
</div>

{#if mapped.length === 0}
	<p class="text-sm text-ink-muted">
		No replicated disks{project ? ' in this project' : ''} yet. Pick a replicated class when you add a
		disk.
	</p>
{:else}
	<Card title="Where every copy lives" flush>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="overflow-x-auto" role="region" aria-label="Copy placement" tabindex="0">
			<table class="w-full border-separate border-spacing-0 text-[13px]">
				<thead>
					<tr>
						<th scope="col" class="{TH} {STICKY}"
							>{mapped.some((r) => r.vm) ? 'VM' : 'Claim'} and disk</th
						>
						{#each hosts as h (h)}
							<th scope="col" class="border-b border-line p-0" class:down={h === down}>
								<button
									type="button"
									aria-pressed={h === down}
									onclick={() => toggle(h)}
									class="flex w-full min-w-[84px] cursor-pointer flex-col items-center gap-1 px-2 py-2 hover:bg-select-soft"
								>
									<span class="font-mono text-xs font-medium text-ink">{h}</span>
									<span class="block h-1 w-12 overflow-hidden rounded-sm bg-inset-strong">
										<span
											class="block h-full {used(h) > 70 ? 'bg-warn' : 'bg-glyph'}"
											style:width="{used(h)}%"
										></span>
									</span>
									<span
										class="text-[11px] font-normal {h === down
											? 'text-danger-ink'
											: 'text-ink-faint'}"
									>
										{h === down ? 'Down, rehearsal' : `${used(h)}% of pools`}
									</span>
								</button>
							</th>
						{/each}
						<th scope="col" class="{TH} min-w-[230px] text-left">
							{down ? `If ${down} goes down` : 'Protection'}
						</th>
					</tr>
				</thead>
				{#each mapped as r (r.key)}
					{@const fate = down ? rowIfDown(r, down) : null}
					<tbody>
						<tr class="bg-inset">
							<th
								scope="rowgroup"
								class="sticky left-0 z-10 bg-inset px-3 py-1.5 text-left font-normal"
							>
								{#if r.vm}
									<a
										class="font-bold text-accent-ink hover:underline"
										href={vmHref(r.vm.namespace, r.vm.name)}
									>
										{r.name}
									</a>
								{:else}
									<span class="font-bold text-ink">{r.name}</span>
								{/if}
								<span class="ml-1.5 text-xs text-ink-faint">{r.project}</span>
							</th>
							{#each hosts as h (h)}
								<td
									class="border-b border-line-soft px-1 py-1.5 text-center"
									class:down={h === down}
								>
									{#if h === r.runsOn}
										<span
											class="inline-block rounded px-1.5 text-[11px] font-bold text-white {h ===
											down
												? 'bg-danger-deep'
												: 'bg-accent'}"
											title="{r.name} runs on {h}"
										>
											{r.vm ? 'VM' : 'In use'}
										</span>
									{/if}
								</td>
							{/each}
							<td class="border-b border-line-soft px-3 py-1.5 text-xs font-medium">
								{#if fate}
									<span class={TONE_TEXT[FATE_TONE[fate.fate]]}>{fate.text}</span>
								{:else if r.runsOn}
									<span class="text-ink-faint">Runs on {r.runsOn}</span>
								{/if}
							</td>
						</tr>
						{#each r.disks as d (d.volume.name)}
							{@const now = down ? null : diskNow(d.volume, r.runsOn)}
							<tr>
								<th scope="row" class="{CELL} {STICKY} pl-7 font-normal">
									<span class="font-mono text-xs">{d.label}</span>
									<span class="ml-1.5 text-xs text-ink-faint"
										>{size(d.volume.sizeBytes)}, {d.volume.tier}</span
									>
								</th>
								{#each hosts as h (h)}
									{@const c = cellFor(d.volume, h, r.runsOn)}
									<td class={CELL} class:down={h === down}>
										<CopyGlyph
											kind={c.kind}
											percent={c.percent}
											label={c.label}
											down={h === down}
										/>
									</td>
								{/each}
								<td class="border-b border-line-soft px-3 py-1.5 text-xs">
									{#if now}
										<span class={now.tone === 'neutral' ? 'text-ink-muted' : TONE_TEXT[now.tone]}>
											{now.text}
										</span>
										{#if d.volume.splitBrain && !d.volume.resolving && repair.ok}
											<button
												type="button"
												class="ml-2 cursor-pointer font-medium text-accent-ink hover:underline"
												aria-label="Keep one copy of {r.name} {d.label}"
												onclick={() => (splitOpen = { volume: d.volume, name: r.name })}
											>
												Keep one
											</button>
										{/if}
									{:else}
										{@const out = diskIfDown(d.volume, down)}
										<span class={TONE_TEXT[FATE_TONE[out.fate]]}>{out.text}</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				{/each}
			</table>
		</div>
		<div
			class="flex flex-wrap items-center gap-x-5 gap-y-1.5 border-t border-line-soft px-card py-2.5 text-xs text-ink-muted"
		>
			<span class="inline-flex items-center gap-1.5"><CopyGlyph kind="copy" />Copy</span>
			<span class="inline-flex items-center gap-1.5"
				><CopyGlyph kind="catching" percent={60} />Catching up</span
			>
			<span class="inline-flex items-center gap-1.5"
				><CopyGlyph kind="remote" />No copy, reads over the network</span
			>
			<span class="inline-flex items-center gap-1.5"
				><CopyGlyph kind="witness" />Witness, holds no data</span
			>
			<span class="inline-flex items-center gap-1.5"><CopyGlyph kind="lost" />Out of reach</span>
		</div>
	</Card>
{/if}

{#if splitOpen}
	<SplitBrainModal
		volume={splitOpen.volume}
		name={splitOpen.name}
		onclose={() => (splitOpen = null)}
	/>
{/if}
{#if moveOff}
	<MoveCopiesModal host={moveOff} {nameOf} onclose={() => (moveOff = '')} />
{/if}

<style>
	.down {
		background-image: repeating-linear-gradient(
			135deg,
			var(--color-danger-soft) 0 4px,
			transparent 4px 9px
		);
	}
</style>
