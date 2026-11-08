<script lang="ts">
	import { api } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Consequence from './Consequence.svelte';
	import StageChip from './StageChip.svelte';
	import StageModal from './StageModal.svelte';

	let { project, onclose }: { project: string; onclose: () => void } = $props();

	const gate = $derived(clusterGate.syncAccess());
	// svelte-ignore state_referenced_locally
	const name = project;
</script>

<StageModal
	title="Grant sync access to {project}"
	label="Stage grant"
	missing={gate.ok ? [] : [gate.why]}
	onsubmit={() => api.syncAccess(name)}
	onresult={() => ui.toastStaged(`Sync access for ${name} staged in the platform draft.`)}
	{onclose}
>
	<div class="flex items-center gap-2">
		<span class="text-ink-soft">Stages in the platform repo</span>
		<StageChip kind="pr" />
	</div>
	<div class="flex flex-col gap-1.5">
		<Consequence tone="info"
			>Each of {project}'s namespaces grants its sync account admin, so ArgoCD syncs it as that
			account.</Consequence
		>
		<Consequence>Nothing changes until the platform pull request merges.</Consequence>
	</div>
</StageModal>
