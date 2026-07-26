<script lang="ts">
	import type { Snippet } from 'svelte';

	// The one callout box: tinted + inked per tone, bg-inset for the neutral
	// explainer. Class maps are literal records so Tailwind sees static strings.
	// Layout context (margins, flex) comes from the caller via class.
	let {
		tone,
		border = false,
		class: cls = '',
		children,
	}: {
		tone: 'warn' | 'danger' | 'ok' | 'accent' | 'neutral';
		border?: boolean;
		class?: string;
		children: Snippet;
	} = $props();

	const TINT: Record<string, string> = {
		warn: 'bg-warn-soft/60 text-warn-ink',
		danger: 'bg-danger-soft/60 text-danger-ink',
		ok: 'bg-ok-soft/60 text-ok-ink',
		accent: 'bg-accent-soft/60 text-accent-ink',
		neutral: 'bg-inset text-ink-muted',
	};
	const EDGE: Record<string, string> = {
		warn: 'border border-warn-soft',
		danger: 'border border-danger-soft',
		ok: 'border border-ok-soft',
		accent: 'border border-accent-soft',
		neutral: 'border border-line',
	};
</script>

<div class="rounded px-3 py-2 text-xs {TINT[tone]} {border ? EDGE[tone] : ''} {cls}">
	{@render children()}
</div>
