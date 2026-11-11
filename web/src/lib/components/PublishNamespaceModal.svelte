<script lang="ts">
	import { api } from '#lib/api.js';
	import { projectNS, publishGate } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Consequence from './Consequence.svelte';
	import StageChip from './StageChip.svelte';
	import StageModal from './StageModal.svelte';

	let { project, onclose }: { project: string; onclose: () => void } = $props();

	const p = $derived(inventory.projects.find((x) => x.name === project));
	const first = $derived(projectNS(p ?? { namespaces: [] }));
	const gate = $derived(publishGate.namespace(first));
	const ns = $derived(caps.ns(first)?.project?.publishNamespace ?? '');
	// svelte-ignore state_referenced_locally
	const name = project;
</script>

<StageModal
	title={`Create ${ns}`}
	label="Stage namespace"
	missing={gate.ok ? [] : [gate.why]}
	onsubmit={() => api.publishNamespace(name)}
	onresult={() => ui.toastStaged(`${ns} staged in the platform draft.`)}
	{onclose}
>
	<div class="flex items-center gap-2">
		<span class="text-ink-soft">Stages in the platform repo</span>
		<StageChip kind="pr" />
	</div>
	<div class="flex flex-col gap-1.5">
		<Consequence tone="info"
			><span class="font-mono">{ns}</span> will hold {project}'s Gateway. {project}'s people and its
			repo get no write there.</Consequence
		>
		<Consequence
			>It joins no VM Network: the Gateway serves {project}'s namespaces on the cluster's own
			network.</Consequence
		>
		<Consequence>Turn publishing on once the platform pull request merges.</Consequence>
	</div>
</StageModal>
