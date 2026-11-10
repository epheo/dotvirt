<script lang="ts">
	import { api } from '#lib/api.js';
	import { publishGate } from '#lib/gates.js';
	import { modules } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Consequence from './Consequence.svelte';
	import StageChip from './StageChip.svelte';
	import StageModal from './StageModal.svelte';

	let { project, namespace, onclose }: { project: string; namespace: string; onclose: () => void } =
		$props();

	const gate = $derived(publishGate.on(namespace));
	const host = $derived(`*.${namespace}.${modules.value?.publishDomain ?? ''}`);
	// svelte-ignore state_referenced_locally
	const [name, ns] = [project, namespace];
</script>

<StageModal
	title="Turn publishing on in {namespace}"
	label="Stage publishing"
	missing={gate.ok ? [] : [gate.why]}
	onsubmit={() => api.publishing(name, ns)}
	onresult={() => ui.toastStaged(`Publishing for ${ns} staged in the platform draft.`)}
	{onclose}
>
	<div class="flex items-center gap-2">
		<span class="text-ink-soft">Stages in the platform repo</span>
		<StageChip kind="pr" />
	</div>
	<div class="flex flex-col gap-1.5">
		<Consequence tone="info"
			>{namespace} gets its own Gateway, answering <span class="font-mono">{host}</span> and only
			routes from {namespace}.</Consequence
		>
		<Consequence tone="info">{namespace}'s admins may then publish its VMs there.</Consequence>
		<Consequence
			>Point <span class="font-mono">{host}</span> at the Gateway's address once it has one.</Consequence
		>
		<Consequence>Nothing changes until the platform pull request merges.</Consequence>
	</div>
</StageModal>
