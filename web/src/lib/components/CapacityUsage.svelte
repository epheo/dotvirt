<script lang="ts">
	import type { VMUsage } from '$lib/api';
	import { relativeAge } from '$lib/format';
	import UsageBar from './UsageBar.svelte';

	// Pure view over the summary's usage snapshot — VMSummary owns the fetch so
	// the at-a-glance tiles and these bars read one consistent sample.
	let { usage, loading, failed }: { usage: VMUsage | null; loading: boolean; failed: boolean } =
		$props();
</script>

<section class="rounded border border-line">
	<h3
		class="flex items-center justify-between border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
	>
		<span>Capacity &amp; usage</span>
		{#if usage}<span class="font-normal text-ink-faint normal-case"
				>updated {relativeAge(usage.updated)}</span
			>{/if}
	</h3>
	<div class="space-y-3 p-3">
		{#if usage}
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
</section>
