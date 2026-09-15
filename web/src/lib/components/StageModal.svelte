<script lang="ts">
	import type { Snippet } from 'svelte';
	import { action } from '$lib/resource.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

	// The staging dialogs' shared shell: Modal + scrollable body + ErrorNote +
	// StageFooter, owning the submit action() every dialog repeated. The
	// form-specific missing/summary derivations stay in each dialog - they ARE
	// the form; this owns only what happens around them. onsubmit stages the
	// request; success raises the staged toast, then closes.
	//
	// Every dialog mounts fresh per open (the shell renders one ui.modal and
	// closes it on selection change), so a form field seeded from a prop at
	// init is the intent; that is what each svelte-ignore
	// state_referenced_locally in the dialogs stands for.
	let {
		title,
		size = 'md',
		label,
		missing = [],
		summary = '',
		onsubmit,
		onstaged,
		onclose,
		icon,
		children,
	}: {
		title: string;
		size?: 'md' | 'lg' | '3xl';
		label: string;
		missing?: string[];
		summary?: string;
		// The staging call's response is irrelevant here: success means "staged".
		onsubmit: () => Promise<unknown>;
		// For a host that must refresh a view of its own after the stage.
		onstaged?: () => void;
		onclose: () => void;
		icon?: Snippet;
		children: Snippet;
	} = $props();

	const op = action();

	async function submit() {
		if (missing.length) return;
		if (await op.run(onsubmit)) {
			ui.toastStaged();
			onstaged?.();
			onclose();
		}
	}
</script>

<Modal {title} {size} {onclose} {icon}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		{@render children()}
		<ErrorNote error={op.error} />
	</div>
	{#snippet footer()}
		<StageFooter
			{label}
			{summary}
			{missing}
			disabled={missing.length > 0}
			submitting={op.busy}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
