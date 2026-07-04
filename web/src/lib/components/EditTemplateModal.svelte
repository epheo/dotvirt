<script lang="ts">
	import { BookCopy } from 'lucide-svelte';
	import { api, Unauthorized, type Template } from '$lib/api';
	import Modal from './Modal.svelte';

	// Edit a content-library item: the template is a manifest in the library's
	// git repo, so editing is replacing that file — staged into Changes and
	// applied when the library's PR merges (vSphere's check-out/check-in, with
	// the review happening on the PR). The server rejects content that no longer
	// parses as a VirtualMachineTemplate, so an edit can't break the catalog.
	let {
		template,
		onclose,
		onstaged,
	}: {
		template: Template;
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// The buffer seeds from the item the modal opened for (the host closes it on
	// selection change, so the initial capture is the intent).
	// svelte-ignore state_referenced_locally
	let yaml = $state(template.yaml);
	let busy = $state(false);
	let error = $state('');

	const libraryLabel = $derived(
		template.library === 'platform' ? 'Shared library' : template.library,
	);
	const dirty = $derived(yaml !== template.yaml);

	async function save() {
		busy = true;
		error = '';
		try {
			await api.updateTemplate({ library: template.library, name: template.name, yaml });
			onstaged();
			onclose();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			busy = false;
		}
	}
</script>

<Modal title="Edit Template — {template.name}" size="3xl" {onclose}>
	{#snippet icon()}<BookCopy size={16} class="text-ink-muted" />{/snippet}
	<div class="min-h-0 space-y-3 overflow-y-auto px-5 py-4 text-sm">
		<p class="text-xs text-ink-faint">
			{libraryLabel} / {template.sourceFile} — the manifest below replaces the file when the PR merges.
			Deployed VMs are unaffected; only new deploys pick up the change.
		</p>
		<textarea
			bind:value={yaml}
			rows="24"
			spellcheck="false"
			class="w-full resize-y rounded border border-line bg-inset px-3 py-2 font-mono text-xs leading-relaxed text-ink"
		></textarea>
		{#if error}
			<pre
				class="rounded bg-danger-soft/60 p-2 text-xs whitespace-pre-wrap text-danger-ink">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<button
			onclick={onclose}
			class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong">Cancel</button
		>
		<button
			onclick={save}
			disabled={!dirty || !yaml.trim() || busy}
			class="rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-line-strong"
			>Stage edit</button
		>
	{/snippet}
</Modal>
