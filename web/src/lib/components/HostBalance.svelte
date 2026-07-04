<script lang="ts">
	import { api, Unauthorized, type HostLoad } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';
	import InfoCard from './InfoCard.svelte';

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

	// Histogram geometry: percent maps onto x (0-100% → 0-W), counts onto bar
	// height against the tallest bucket.
	const W = 320;
	const TOP = 12;
	const BASE = 66;
	const px = (pct: number) => Math.min(100, Math.max(0, pct)) * (W / 100);

	const maxBucket = $derived(data ? Math.max(1, ...data.buckets) : 1);
	const barColor = (i: number): string => {
		if (!data?.band) return 'var(--chart-axis)';
		const mid = i * 10 + 5;
		if (mid > data.band.high) return 'var(--color-warn)';
		if (mid < data.band.low) return 'var(--chart-cold)';
		return 'var(--chart-axis)';
	};
	const barH = (n: number) => (n / maxBucket) * (BASE - TOP);
	// Sources to name: the hottest workers, but only those actually above the band.
	const hot = $derived(
		data?.band ? data.hottest.filter((o) => o.pct > data!.band!.high).slice(0, 5) : [],
	);
</script>

{#if data}
	<InfoCard title="Host balance">
		{#snippet action()}
			<a href="?tab=configure" class="text-xs text-blue-700 hover:underline">
				{data?.band ? 'DRS settings' : 'Enable DRS'}
			</a>
		{/snippet}

		<div class="p-3">
			<svg
				viewBox="0 0 {W} 78"
				class="w-full max-w-xl"
				role="img"
				aria-label="Worker utilization distribution"
			>
				{#if data.band}
					<rect
						x={px(data.band.low)}
						y={TOP}
						width={px(data.band.high) - px(data.band.low)}
						height={BASE - TOP}
						style:fill="var(--chart-band)"
						opacity="0.09"
					/>
				{/if}
				{#each data.buckets as n, i (i)}
					{#if n > 0}
						<rect
							x={i * (W / 10) + 3}
							y={BASE - barH(n)}
							width={W / 10 - 6}
							height={barH(n)}
							rx="1.5"
							style:fill={barColor(i)}
						>
							<title>{i * 10}–{i * 10 + 10}%: {n} worker{n === 1 ? '' : 's'}</title>
						</rect>
						<text
							x={i * (W / 10) + W / 20}
							y={BASE - barH(n) - 3}
							text-anchor="middle"
							class="fill-slate-500"
							font-size="8">{n}</text
						>
					{/if}
				{/each}
				<line
					x1={px(data.mean)}
					y1={TOP - 4}
					x2={px(data.mean)}
					y2={BASE}
					style:stroke="var(--chart-mean)"
					stroke-dasharray="2 2"
				/>
				<line x1="0" y1={BASE} x2={W} y2={BASE} style:stroke="var(--chart-track-strong)" />
				{#each [0, 25, 50, 75, 100] as t (t)}
					<text
						x={px(t)}
						y="76"
						text-anchor={t === 0 ? 'start' : t === 100 ? 'end' : 'middle'}
						class="fill-slate-400"
						font-size="8">{t}%</text
					>
				{/each}
				<text
					x={px(data.mean)}
					y={TOP - 6}
					text-anchor="middle"
					class="fill-slate-600"
					font-size="8">mean {Math.round(data.mean)}%</text
				>
			</svg>

			<div class="mt-2 flex flex-wrap items-center gap-2 text-xs">
				<span class="text-slate-600">
					{data.workers} worker{data.workers === 1 ? '' : 's'}
				</span>
				{#if data.band}
					{#if data.band.above === 0 && data.band.below === 0}
						<span class="rounded bg-green-100 px-1.5 py-0.5 font-medium text-green-800"
							>balanced</span
						>
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
				{:else}
					<span class="text-slate-400">DRS not configured — no action band.</span>
				{/if}
			</div>

			{#if hot.length}
				<ul class="mt-2 space-y-1">
					{#each hot as o (o.node)}
						<li class="flex items-center gap-2 text-xs">
							<a
								href="/hosts/{o.node}"
								class="w-44 min-w-0 truncate text-slate-700 hover:text-blue-700 hover:underline"
								>{o.node}</a
							>
							<span class="h-1.5 flex-1 overflow-hidden rounded bg-slate-100">
								<span
									class="block h-full rounded bg-amber-400"
									style="width: {Math.min(100, o.pct)}%"
								></span>
							</span>
							<span class="w-9 shrink-0 text-right font-medium text-amber-700"
								>{Math.round(o.pct)}%</span
							>
							{#if o.unschedulable}
								<span class="shrink-0 text-slate-400" title="cordoned">⊘</span>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	</InfoCard>
{/if}
