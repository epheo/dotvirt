<script lang="ts">
	import type { Snippet } from 'svelte';

	// Note's full-bleed sibling: the ambient strip above content (stream errors,
	// pending changes, SSO/repo prompts). Same tone maps, but edge-to-edge with a
	// bottom border. Buttons/links stay the caller's children.
	let {
		tone,
		size = 'sm',
		class: cls = '',
		children,
	}: {
		tone: 'warn' | 'danger' | 'ok' | 'accent' | 'neutral';
		size?: 'sm' | 'md';
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
		warn: 'border-warn-soft',
		danger: 'border-danger-soft',
		ok: 'border-ok-soft',
		accent: 'border-accent-soft',
		neutral: 'border-line',
	};
</script>

<div
	class="flex items-center gap-2 border-b px-4 {size === 'md'
		? 'py-2 text-sm'
		: 'py-1.5 text-xs'} {TINT[tone]} {EDGE[tone]} {cls}"
>
	{@render children()}
</div>
