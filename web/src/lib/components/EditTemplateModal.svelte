<script lang="ts">
	import { BookCopy } from 'lucide-svelte';
	import { api, type Template } from '$lib/api';
	import StageModal from './StageModal.svelte';

	// Edit a content-library item: the template is a manifest in the library's
	// git repo, so editing is replacing that file - staged into Changes and
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

	const libraryLabel = $derived(
		template.library === 'platform' ? 'Shared library' : template.library,
	);
	const dirty = $derived(yaml !== template.yaml);
	const missing = $derived(
		!yaml.trim() ? ['The manifest is empty'] : dirty ? [] : ['No changes yet'],
	);
</script>

<StageModal
	title="Edit Template — {template.name}"
	size="3xl"
	label="Stage edit"
	{missing}
	summary={`Replaces ${template.sourceFile} in the ${libraryLabel.toLowerCase()}`}
	onsubmit={() => api.updateTemplate({ library: template.library, name: template.name, yaml })}
	{onstaged}
	{onclose}
>
	{#snippet icon()}<BookCopy size={16} class="text-ink-muted" />{/snippet}
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
</StageModal>
