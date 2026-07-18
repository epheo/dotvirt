<script lang="ts">
	import { api, Unauthorized, type HostLoad, type HostWorker } from '$lib/api';
	import { densityPath, layoutStrip } from '$lib/dotstrip';
	import { relativeAge } from '$lib/format';
	import { pollWhileVisible } from '$lib/poll';
	import { inventory } from '$lib/state/inventory.svelte';
	import InfoCard from './InfoCard.svelte';

	// Worker utilization with the DRS action band: every worker is a dot on a
	// 0-100% axis (CPU, plus a slim memory strip), so a two-worker lab reads as
	// a labeled comparison while a spread-out fleet reads as a distribution.
	// When stacks outgrow the strip the dots give way to a density silhouette
	// and only out-of-band workers stay individually drawn — the hand-off is
	// geometric (see dotstrip.ts), never a node-count threshold.
	let data = $state<HostLoad | null>(null);

	async function load() {
		try {
			data = await api.hostLoad();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			data = null; // metrics off or no worker series: the card simply absents itself
		}
	}
	// pollWhileVisible only paces refreshes — the initial load is the caller's,
	// or the card sits empty until the first 30s tick.
	$effect(() => {
		void load();
		return pollWhileVisible(load, 30000);
	});

	const W = 640;
	const CPU_H = 102;
	const MEM_H = 32;
	const CPU_TOP = 16;
	const MEM_TOP = 146;
	const px = (pct: number) => (Math.min(100, Math.max(0, pct)) * W) / 100;

	const nodes = $derived(data?.nodes ?? []);
	const band = $derived(data?.band);
	const cpu = $derived(
		layoutStrip(
			nodes.map((n) => n.pct),
			W,
			CPU_H,
		),
	);
	const mem = $derived(
		layoutStrip(
			nodes.map((n) => n.mem ?? 0),
			W,
			MEM_H,
		),
	);
	const memKnown = $derived(nodes.some((n) => (n.mem ?? 0) > 0));
	const vbH = $derived(memKnown ? 192 : 133);
	const axisY = $derived(memKnown ? 189 : 130);

	const cpuColor = (n: HostWorker): string => {
		if (!band) return 'var(--chart-axis)';
		if (n.pct > band.high) return 'var(--color-warn)';
		if (n.pct < band.low) return 'var(--chart-cold)';
		return 'var(--chart-axis)';
	};
	// Memory has no DRS band; >90% is worth flagging on any host regardless.
	const memColor = (n: HostWorker): string =>
		(n.mem ?? 0) > 90 ? 'var(--color-warn)' : 'var(--chart-axis)';
	const title = (n: HostWorker): string =>
		`${n.node} · CPU ${Math.round(n.pct)}%` +
		(n.mem ? ` · mem ${Math.round(n.mem)}%` : '') +
		(n.unschedulable ? ' · cordoned' : '');

	// Density mode keeps individual dots only for out-of-band workers, worst
	// first, capped so a pathological fleet cannot flood the strip.
	const hotDots = $derived(
		band && !cpu.dots ? nodes.filter((n) => n.pct > band.high).slice(0, 14) : [],
	);
	const coldDots = $derived(
		band && !cpu.dots ? nodes.filter((n) => n.pct < band.low).slice(-14) : [],
	);
	const memHotDots = $derived(!mem.dots ? nodes.filter((n) => (n.mem ?? 0) > 90).slice(0, 14) : []);
	const memPressure = $derived(nodes.filter((n) => (n.mem ?? 0) > 90).length);

	// Direct labels only when every name can plausibly fit; the rows below
	// carry the full roster for small fleets anyway. One label per stack, on
	// two alternating levels when neighbors would collide.
	const labels = $derived.by(() => {
		if (!data || !cpu.dots || data.workers > 8) return [];
		const cols = new Map<number, { x: number; y: number; i: number; extra: number }>();
		for (const d of cpu.dots) {
			const c = cols.get(d.x);
			if (!c) cols.set(d.x, { x: d.x, y: d.y - d.r, i: d.i, extra: 0 });
			else {
				c.extra++;
				if (d.y - d.r < c.y) {
					c.y = d.y - d.r;
					c.i = d.i;
				}
			}
		}
		const ends = [-Infinity, -Infinity];
		const out: { x: number; y: number; i: number; text: string }[] = [];
		for (const c of [...cols.values()].sort((a, b) => a.x - b.x)) {
			const name = nodes[c.i].node;
			const text =
				(name.length > 16 ? name.slice(0, 15) + '…' : name) + (c.extra ? ` +${c.extra}` : '');
			const half = (text.length * 5.5) / 2;
			const x = Math.min(W - half, Math.max(half, c.x));
			const lvl = x - half > ends[0] + 6 ? 0 : x - half > ends[1] + 6 ? 1 : -1;
			if (lvl < 0) continue;
			ends[lvl] = x + half;
			out.push({ x, y: Math.max(8, c.y - (lvl ? 16 : 4)), i: c.i, text });
		}
		return out;
	});

	// The roster: everyone for small fleets, out-of-band workers for large
	// ones — a balanced 500-node fleet needs no rows at all.
	const rows = $derived.by(() => {
		if (!data) return [];
		if (data.workers <= 10) return data.nodes;
		if (!band) return data.nodes.slice(0, 3);
		return [
			...nodes.filter((n) => n.pct > band.high).slice(0, 5),
			...nodes.filter((n) => n.pct < band.low).slice(-5),
		];
	});
	const unlisted = $derived(data ? data.workers - rows.length : 0);
	const pctClass = (n: HostWorker): string => {
		if (!band) return 'text-ink-soft';
		if (n.pct > band.high) return 'text-warn-ink';
		if (n.pct < band.low) return 'text-cold-ink';
		return 'text-ink-soft';
	};

	// Live migrations from the inventory stream: each VM carries its last move,
	// so recent activity (DRS, maintenance, or manual) needs no extra endpoint.
	const DAY = 24 * 3600 * 1000;
	const moves = $derived.by(() => {
		const now = Date.now();
		return inventory.allVMs
			.filter(
				(v) =>
					v.migration?.completed &&
					v.migration.endedAt &&
					now - Date.parse(v.migration.endedAt) < DAY,
			)
			.sort((a, b) => Date.parse(b.migration!.endedAt!) - Date.parse(a.migration!.endedAt!));
	});
</script>

{#if data}
	<InfoCard title="Host balance">
		{#snippet action()}
			<a href="?tab=configure" class="text-xs text-accent-ink hover:underline">
				{band ? 'DRS settings' : 'Enable DRS'}
			</a>
		{/snippet}

		<div class="p-3">
			<svg
				viewBox="0 0 {W} {vbH}"
				class="w-full max-w-2xl"
				role="img"
				aria-label="Worker utilization distribution"
			>
				<text x="0" y="10" class="fill-ink-faint" font-size="9">CPU</text>
				{#if band}
					<rect
						x={px(band.low)}
						y={CPU_TOP}
						width={px(band.high) - px(band.low)}
						height={CPU_H}
						style:fill="var(--chart-band)"
						opacity="0.09"
					/>
				{/if}
				<g transform="translate(0 {CPU_TOP})">
					{#if cpu.dots}
						{#each cpu.dots as d (d.i)}
							{@const n = nodes[d.i]}
							<a href="/hosts/{n.node}">
								{#if n.unschedulable}
									<circle
										cx={d.x}
										cy={d.y}
										r={Math.max(1.2, d.r - 0.75)}
										style:fill="var(--color-panel)"
										style:stroke={cpuColor(n)}
										stroke-width="1.5"
									/>
								{:else}
									<circle cx={d.x} cy={d.y} r={d.r} style:fill={cpuColor(n)} />
								{/if}
								<title>{title(n)}</title>
							</a>
						{/each}
					{:else}
						<path
							d={densityPath(cpu.bins, W, CPU_H - 14, CPU_H)}
							style:fill="var(--chart-axis)"
							opacity="0.3"
						/>
						{#each [...hotDots, ...coldDots] as n (n.node)}
							<a href="/hosts/{n.node}">
								<circle cx={px(n.pct)} cy={CPU_H - 4} r="3" style:fill={cpuColor(n)} />
								<title>{title(n)}</title>
							</a>
						{/each}
					{/if}
					{#each labels as l (l.i)}
						<text x={l.x} y={l.y} text-anchor="middle" class="fill-ink-soft" font-size="9"
							>{l.text}</text
						>
					{/each}
				</g>
				<line
					x1={px(data.mean)}
					y1={CPU_TOP - 4}
					x2={px(data.mean)}
					y2={CPU_TOP + CPU_H}
					style:stroke="var(--chart-mean)"
					stroke-dasharray="2 2"
				/>
				<text
					x={Math.min(W - 30, Math.max(42, px(data.mean)))}
					y="10"
					text-anchor="middle"
					class="fill-ink-soft"
					font-size="9">mean {Math.round(data.mean)}%</text
				>
				<line
					x1="0"
					y1={CPU_TOP + CPU_H}
					x2={W}
					y2={CPU_TOP + CPU_H}
					style:stroke="var(--chart-track-strong)"
				/>

				{#if memKnown}
					<text x="0" y="140" class="fill-ink-faint" font-size="9">Memory</text>
					<g transform="translate(0 {MEM_TOP})">
						{#if mem.dots}
							{#each mem.dots as d (d.i)}
								{@const n = nodes[d.i]}
								<a href="/hosts/{n.node}">
									<circle cx={d.x} cy={d.y} r={d.r} style:fill={memColor(n)} />
									<title>{title(n)}</title>
								</a>
							{/each}
						{:else}
							<path
								d={densityPath(mem.bins, W, MEM_H - 6, MEM_H)}
								style:fill="var(--chart-axis)"
								opacity="0.3"
							/>
							{#each memHotDots as n (n.node)}
								<a href="/hosts/{n.node}">
									<circle cx={px(n.mem ?? 0)} cy={MEM_H - 4} r="3" style:fill="var(--color-warn)" />
									<title>{title(n)}</title>
								</a>
							{/each}
						{/if}
					</g>
					<line
						x1="0"
						y1={MEM_TOP + MEM_H}
						x2={W}
						y2={MEM_TOP + MEM_H}
						style:stroke="var(--chart-track)"
					/>
				{/if}

				{#each [0, 25, 50, 75, 100] as t (t)}
					<text
						x={px(t)}
						y={axisY}
						text-anchor={t === 0 ? 'start' : t === 100 ? 'end' : 'middle'}
						class="fill-ink-faint"
						font-size="8">{t}%</text
					>
				{/each}
			</svg>

			<div class="mt-2 flex flex-wrap items-center gap-2 text-xs">
				<span class="text-ink-soft">
					{data.workers} worker{data.workers === 1 ? '' : 's'}
				</span>
				{#if band}
					{#if band.above === 0 && band.below === 0}
						<span class="rounded bg-ok-soft px-1.5 py-0.5 font-medium text-ok-ink">balanced</span>
					{:else}
						{#if band.above > 0}
							<span
								class="rounded bg-warn-soft px-1.5 py-0.5 font-medium text-warn-ink"
								title="above the DRS band — migration sources">{band.above} hot</span
							>
						{/if}
						{#if band.below > 0}
							<span
								class="rounded bg-cold-soft px-1.5 py-0.5 font-medium text-cold-ink"
								title="below the DRS band — migration targets">{band.below} cold</span
							>
						{/if}
					{/if}
					<span class="text-ink-faint" title="the configured DRS deviation window around the mean">
						band {Math.round(band.low)}–{Math.round(band.high)}%
					</span>
				{:else}
					<span class="text-ink-faint">DRS not configured — no action band.</span>
				{/if}
				{#if memPressure > 0}
					<span
						class="rounded bg-warn-soft px-1.5 py-0.5 font-medium text-warn-ink"
						title="workers above 90% memory">{memPressure} high mem</span
					>
				{/if}
			</div>

			{#if moves.length}
				{@const latest = moves[0]}
				<p class="mt-1 text-xs text-ink-faint">
					{moves.length} live migration{moves.length === 1 ? '' : 's'} in the last 24h · latest
					{latest.name}: {latest.migration?.sourceNode} → {latest.migration?.targetNode}
					({relativeAge(latest.migration?.endedAt)})
				</p>
			{:else if band}
				<p class="mt-1 text-xs text-ink-faint">No live migrations in the last 24h.</p>
			{/if}

			{#if rows.length}
				<ul class="mt-2 space-y-1">
					{#each rows as n (n.node)}
						<li class="flex items-center gap-2 text-xs">
							<a
								href="/hosts/{n.node}"
								class="w-40 min-w-0 truncate text-ink-soft hover:text-accent-ink hover:underline"
								>{n.node}</a
							>
							{#if n.unschedulable}
								<span class="shrink-0 text-ink-faint" title="cordoned">⊘</span>
							{/if}
							<span class="relative h-1.5 flex-1 overflow-hidden rounded bg-inset-strong">
								<span
									class="block h-full rounded"
									style="width: {Math.min(100, n.pct)}%; background: {cpuColor(n)}"
								></span>
								{#if band}
									<span class="absolute inset-y-0 w-px bg-ink-faint/60" style="left: {band.low}%"
									></span>
									<span class="absolute inset-y-0 w-px bg-ink-faint/60" style="left: {band.high}%"
									></span>
								{/if}
							</span>
							<span class="w-9 shrink-0 text-right font-medium {pctClass(n)}"
								>{Math.round(n.pct)}%</span
							>
							{#if memKnown}
								<span class="hidden h-1.5 w-24 overflow-hidden rounded bg-inset-strong sm:block">
									<span
										class="block h-full rounded"
										style="width: {Math.min(100, n.mem ?? 0)}%; background: {memColor(n)}"
									></span>
								</span>
								<span class="hidden w-9 shrink-0 text-right text-ink-muted sm:block"
									>{Math.round(n.mem ?? 0)}%</span
								>
							{/if}
						</li>
					{/each}
				</ul>
				{#if unlisted > 0}
					<p class="mt-1 text-xs text-ink-faint">
						{unlisted} more worker{unlisted === 1 ? '' : 's'}
						{band ? 'within the band' : ''}
					</p>
				{/if}
			{/if}
		</div>
	</InfoCard>
{/if}
