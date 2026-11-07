<script lang="ts">
	import { GitPullRequest, Zap } from '@lucide/svelte';

	// Says how the action beside it reaches the cluster: through a pull request,
	// right away, or already staged and waiting to be proposed.
	let { kind }: { kind: 'pr' | 'now' | 'staged' } = $props();

	const CHIP = {
		pr: {
			label: 'Pull request',
			hint: 'Stages into a pull request. Nothing changes until it merges.',
		},
		now: { label: 'Runs now', hint: 'Acts on the cluster right away. No pull request.' },
		staged: { label: 'staged', hint: 'Staged in your draft. Not proposed yet.' },
	};
	const chip = $derived(CHIP[kind]);
</script>

<span
	title={chip.hint}
	class="inline-flex h-5 shrink-0 items-center gap-1 rounded-full border border-line bg-panel px-2 text-[11px] leading-[18px] whitespace-nowrap text-ink-muted"
>
	{#if kind === 'now'}<Zap size={12} strokeWidth={2} />{:else}<GitPullRequest
			size={12}
			strokeWidth={2}
		/>{/if}{chip.label}
</span>
