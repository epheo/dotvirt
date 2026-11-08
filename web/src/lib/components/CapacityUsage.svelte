<script lang="ts">
	import type { VMUsage } from '#lib/api.js';
	import { relativeAge } from '#lib/format.js';
	import { metricsGap } from '#lib/metrics.js';
	import Card from './Card.svelte';
	import MetricsGap from './MetricsGap.svelte';
	import UsageBar from './UsageBar.svelte';

	// Pure view over the summary's usage snapshot - VMSummary owns the fetch so
	// the at-a-glance tiles and these bars read one consistent sample.
	let { usage, loading, failed }: { usage: VMUsage | null; loading: boolean; failed: boolean } =
		$props();
</script>

<Card title="Capacity &amp; usage" flush>
	{#snippet actions()}
		{#if usage && !metricsGap(usage.unavailable)}<span class="text-ink-faint"
				>updated {relativeAge(usage.updated)}</span
			>{/if}
	{/snippet}
	<div class="space-y-3 px-card py-3">
		{#if usage && metricsGap(usage.unavailable)}
			<MetricsGap reason={usage.unavailable} />
		{:else if usage}
			<MetricsGap reason={usage.unavailable} />
			<UsageBar
				label="CPU"
				used={usage.cpu.used}
				total={100}
				unit="pct"
				color="var(--chart-1)"
				spark={usage.cpu.spark ?? []}
			/>
			<UsageBar
				label="Memory"
				used={usage.memory.used}
				total={usage.memory.total ?? 0}
				unit="bytes"
				color="var(--chart-2)"
				spark={usage.memory.spark ?? []}
			/>
			<UsageBar
				label="Storage (guest)"
				used={usage.storage.used}
				total={usage.storage.total ?? 0}
				unit="bytes"
				color="var(--chart-5)"
				spark={usage.storage.spark ?? []}
			/>
		{:else if loading}
			<p class="text-xs text-ink-faint">Loading usage…</p>
		{:else if failed}
			<p class="text-xs text-ink-faint">Usage metrics unavailable.</p>
		{/if}
	</div>
</Card>
