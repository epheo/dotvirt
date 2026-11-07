<script lang="ts">
	import { goto } from '$app/navigation';
	import { adoptVM } from '#lib/actions.js';
	import type { Issue } from '#lib/issues.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import StatusDot from './StatusDot.svelte';

	// One issue or suggestion: what, where, the cluster's own words, and the one
	// button that fixes it. onleave lets a menu close before either navigates.
	let { issue, onleave }: { issue: Issue; onleave?: () => void } = $props();

	function view() {
		onleave?.();
		goto(issue.href);
	}

	function fix() {
		const a = issue.action;
		onleave?.();
		if ('href' in a) goto(a.href);
		else if ('modal' in a) ui.modal = a.modal;
		else void adoptVM(a.adopt);
	}
</script>

<li class="flex items-center gap-3 px-3 py-2">
	<StatusDot tone={issue.severity} size="sm" />
	<button onclick={view} title={issue.detail ?? ''} class="min-w-0 flex-1 text-left">
		<span class="font-medium text-ink">{issue.scope}</span>
		<span class="text-ink-soft">{issue.label}</span>
		{#if issue.detail}
			<span class="block truncate font-mono text-[11px] text-ink-faint">{issue.detail}</span>
		{/if}
	</button>
	<Button variant="secondary" size="sm" class="shrink-0" onclick={fix}>{issue.action.label}</Button>
</li>
