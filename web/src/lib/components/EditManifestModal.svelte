<script lang="ts">
	import { BookCopy, FileCode } from '@lucide/svelte';
	import { HttpError } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import Button from './Button.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import ManifestDiff from './ManifestDiff.svelte';
	import Note from './Note.svelte';
	import StageModal from './StageModal.svelte';

	// Edit a manifest as the file git holds: the object's own form covers the
	// common shape, this covers the rest (a template, or a network object with
	// settings no form has a field for). Staged into Changes like any edit and
	// applied when the PR merges; the server refuses content that no longer
	// declares the same object, and, given baseBlob (the version the manifest
	// was read at), an edit git has moved past. onreload reads the current
	// version so the edit can be made again on it.
	let {
		title,
		sourceFile,
		yaml: initial,
		baseBlob,
		summary,
		reason = '',
		kind = 'object',
		onsubmit,
		onreload,
		onclose,
	}: {
		title: string;
		sourceFile: string;
		yaml: string;
		baseBlob?: string;
		summary: string; // what staging does, for the footer
		reason?: string; // why the form did not open, when it did not
		kind?: 'template' | 'object';
		onsubmit: (yaml: string, baseBlob?: string) => Promise<unknown>;
		onreload?: () => Promise<{ yaml: string; baseBlob?: string }>;
		onclose: () => void;
	} = $props();

	// svelte-ignore state_referenced_locally
	let base = $state({ yaml: initial, blob: baseBlob });
	// svelte-ignore state_referenced_locally
	let yaml = $state(initial);
	const dirty = $derived(yaml !== base.yaml);
	const missing = $derived(
		!yaml.trim() ? ['The manifest is empty'] : dirty ? [] : ['No changes yet'],
	);

	// The server's refusal of an edit made on a version git has moved past.
	const MOVED = /since the editor loaded it/;
	let moved = $state(false);
	async function submit() {
		moved = false;
		try {
			await onsubmit(yaml, base.blob);
		} catch (e) {
			moved = e instanceof HttpError && e.status === 409 && MOVED.test(e.message);
			throw e;
		}
	}

	// What the user changed on the version they loaded, kept in view once the
	// editor holds the current one, so it can be made again there.
	let earlier = $state<{ before: string; after: string } | null>(null);
	const reloadOp = action();
	async function reload() {
		const edit = { before: base.yaml, after: yaml };
		await reloadOp.run(async () => {
			const fresh = await onreload!();
			earlier = edit;
			base = { yaml: fresh.yaml, blob: fresh.baseBlob };
			yaml = fresh.yaml;
			moved = false;
		});
	}
</script>

<StageModal {title} size="3xl" label="Stage edit" {missing} {summary} onsubmit={submit} {onclose}>
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
	{#if moved && onreload}
		<Note tone="warn" class="flex items-center gap-3">
			<span class="min-w-0 flex-1"
				>{sourceFile} changed in git after you opened it. Reload the current version, then make your edit
				again on it.</span
			>
			<Button size="sm" variant="secondary" disabled={reloadOp.busy} onclick={reload}
				>Reload and re-apply</Button
			>
		</Note>
		<ErrorNote error={reloadOp.error} />
	{/if}
	<textarea
		bind:value={yaml}
		rows="24"
		spellcheck="false"
		aria-label="Manifest"
		class="w-full resize-y rounded border border-line bg-inset px-3 py-2 font-mono text-xs leading-relaxed text-ink"
	></textarea>
	{#if earlier}
		<details open class="rounded border border-line">
			<summary class="cursor-pointer px-3 py-2 text-xs text-ink-muted"
				>Your edit on the version you opened, to make again above</summary
			>
			<ManifestDiff before={earlier.before} after={earlier.after} />
		</details>
	{/if}
</StageModal>
