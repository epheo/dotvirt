<script lang="ts">
	import type { Snippet } from 'svelte';

	// The title row of a page: the object's name, how it stands (a dot and a
	// short line), muted context, and the page's verbs on the right. mono for
	// object names (VMs, disks, networks). Breadcrumb above and TabBar below are
	// the caller's, in a flex column with gap-2.
	type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'off';
	let {
		title,
		mono = false,
		status,
		context,
		actions,
	}: {
		title: string;
		mono?: boolean;
		// text is a snippet when the line carries a link ("Project network of <a>").
		status?: { tone: Tone; text: string | Snippet };
		context?: string | Snippet;
		actions?: Snippet;
	} = $props();

	const DOT: Record<Tone, string> = {
		ok: 'bg-ok',
		warn: 'bg-warn',
		danger: 'bg-danger',
		info: 'bg-accent',
		off: 'bg-ink-faint',
	};
</script>

<div class="flex flex-wrap items-center gap-x-4 gap-y-2">
	<!-- Red Hat Mono ships 400 and 500 only; a synthesized bold smears. -->
	<h1 class="min-w-0 text-2xl leading-8 text-ink {mono ? 'font-mono font-medium' : 'font-bold'}">
		{title}
	</h1>
	{#if status}
		<span class="inline-flex items-center gap-1.5 text-[13px] text-ink-soft">
			<span class="size-2 shrink-0 rounded-full {DOT[status.tone]}"></span>
			{#if typeof status.text === 'string'}{status.text}{:else}{@render status.text()}{/if}
		</span>
	{/if}
	{#if typeof context === 'string'}
		<span class="text-[13px] text-ink-faint">{context}</span>
	{:else if context}
		{@render context()}
	{/if}
	{#if actions}
		<div class="ml-auto flex flex-wrap items-center gap-2">{@render actions()}</div>
	{/if}
</div>
