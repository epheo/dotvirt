<script lang="ts">
	import { BookCopy, FileCode } from 'lucide-svelte';
	import StageModal from './StageModal.svelte';

	// Edit a manifest as the file git holds: the object's own form covers the
	// common shape, this covers the rest (a template, or a network object with
	// settings no form has a field for). Staged into Changes like any edit and
	// applied when the PR merges; the server refuses content that no longer
	// declares the same object.
	let {
		title,
		sourceFile,
		yaml: initial,
		summary,
		reason = '',
		kind = 'object',
		onsubmit,
		onclose,
		onstaged,
	}: {
		title: string;
		sourceFile: string;
		yaml: string;
		summary: string; // what staging does, for the footer
		reason?: string; // why the form did not open, when it did not
		kind?: 'template' | 'object';
		onsubmit: (yaml: string) => Promise<unknown>;
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// The buffer seeds from the file the modal opened for (the host closes it on
	// selection change, so the initial capture is the intent).
	// svelte-ignore state_referenced_locally
	let yaml = $state(initial);
	const dirty = $derived(yaml !== initial);
	const missing = $derived(
		!yaml.trim() ? ['The manifest is empty'] : dirty ? [] : ['No changes yet'],
	);
</script>

<StageModal
	{title}
	size="3xl"
	label="Stage edit"
	{missing}
	{summary}
	onsubmit={() => onsubmit(yaml)}
	{onstaged}
	{onclose}
>
	{#snippet icon()}
		{#if kind === 'template'}<BookCopy size={16} class="text-ink-muted" />
		{:else}<FileCode size={16} class="text-ink-muted" />{/if}
	{/snippet}
	<p class="text-xs text-ink-faint">
		{#if reason}
			The form cannot open this one: {reason}. The manifest below is
		{:else}
			The manifest below is
		{/if}
		<span class="font-mono">{sourceFile}</span> and replaces that file when the PR merges.
		{#if kind === 'template'}
			Deployed VMs are unaffected; only new deploys pick up the change.
		{/if}
	</p>
	<textarea
		bind:value={yaml}
		rows="24"
		spellcheck="false"
		class="w-full resize-y rounded border border-line bg-inset px-3 py-2 font-mono text-xs leading-relaxed text-ink"
	></textarea>
</StageModal>
