<script lang="ts">
	import { api, Unauthorized, type HostLoad } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';

	// Worker CPU-utilization distribution with the DRS action band — a
	// histogram, not a per-host roster, so a hundreds-of-workers platform
	// renders the same card as a three-worker lab. Only outliers get names.
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

	// Histogram geometry: percent maps 2:1 onto x (0-100% → 0-200px).
	const W = 200;
	const TOP = 6;
	const BASE = 46;
	const px = (pct: number) => Math.min(100, Math.max(0, pct)) * (W / 100);

	const maxBucket = $derived(data ? Math.max(1, ...data.buckets) : 1);
	const barColor = (i: number): string => {
		if (!data?.band) return '#94a3b8';
		const mid = i * 10 + 5;
		if (mid > data.band.high) return '#f59e0b';
		if (mid < data.band.low) return '#38bdf8';
		return '#94a3b8';
	};
	// Sources to name: the hottest workers, but only those actually above the band.
	const hot = $derived(
		data?.band ? data.hottest.filter((o) => o.pct > data!.band!.high).slice(0, 3) : []
	);
</script>

{#if data}
	<div>
		<div class="text-xs font-semibold tracking-wide text-slate-500 uppercase">Host balance</div>
		<div class="mt-1 flex items-start gap-3">
			<svg
				viewBox="0 0 {W} 56"
				class="h-14 w-[200px] shrink-0"
				role="img"
				aria-label="Worker utilization distribution"
			>
				{#if data.band}
					<rect
						x={px(data.band.low)}
						y={TOP}
						width={px(data.band.high) - px(data.band.low)}
						height={BASE - TOP}
						fill="#10b981"
						opacity="0.1"
					/>
				{/if}
				{#each data.buckets as n, i (i)}
					{#if n > 0}
						<rect
							x={i * (W / 10) + 2}
							y={BASE - (n / maxBucket) * (BASE - TOP)}
							width={W / 10 - 4}
							height={(n / maxBucket) * (BASE - TOP)}
							rx="1"
							fill={barColor(i)}
						>
							<title>{i * 10}–{i * 10 + 10}%: {n} worker{n === 1 ? '' : 's'}</title>
						</rect>
					{/if}
				{/each}
				<line x1={px(data.mean)} y1={TOP - 2} x2={px(data.mean)} y2={BASE} stroke="#475569" stroke-dasharray="2 2" />
				<line x1="0" y1={BASE} x2={W} y2={BASE} stroke="#cbd5e1" />
				<text x="0" y="55" class="fill-slate-400" font-size="7">0%</text>
				<text x={W} y="55" text-anchor="end" class="fill-slate-400" font-size="7">100%</text>
				<text x={px(data.mean)} y="55" text-anchor="middle" class="fill-slate-500" font-size="7">
					{Math.round(data.mean)}%
				</text>
			</svg>

			<div class="min-w-0 text-xs">
				<div class="text-slate-600">
					{data.workers} worker{data.workers === 1 ? '' : 's'} · mean {Math.round(data.mean)}%
				</div>
				{#if data.band}
					<div class="mt-1 flex flex-wrap items-center gap-1.5">
						{#if data.band.above === 0 && data.band.below === 0}
							<span class="rounded bg-green-100 px-1.5 py-0.5 font-medium text-green-800">balanced</span>
						{:else}
							{#if data.band.above > 0}
								<span
									class="rounded bg-amber-100 px-1.5 py-0.5 font-medium text-amber-800"
									title="above the DRS band — migration sources">{data.band.above} hot</span
								>
							{/if}
							{#if data.band.below > 0}
								<span
									class="rounded bg-sky-100 px-1.5 py-0.5 font-medium text-sky-800"
									title="below the DRS band — migration targets">{data.band.below} cold</span
								>
							{/if}
						{/if}
						<span class="text-slate-400" title="the configured DRS deviation window around the mean">
							band {Math.round(data.band.low)}–{Math.round(data.band.high)}%
						</span>
					</div>
					{#each hot as o (o.node)}
						<div class="mt-0.5 flex items-baseline gap-1 truncate">
							<a href="/hosts/{o.node}" class="truncate text-slate-700 hover:text-blue-700 hover:underline">
								{o.node}
							</a>
							<span class="shrink-0 text-amber-700">{Math.round(o.pct)}%</span>
							{#if o.unschedulable}
								<span class="shrink-0 text-slate-400" title="cordoned">⊘</span>
							{/if}
						</div>
					{/each}
				{:else}
					<div class="mt-1 text-slate-400">DRS not configured — no action band.</div>
				{/if}
			</div>
		</div>
	</div>
{/if}
