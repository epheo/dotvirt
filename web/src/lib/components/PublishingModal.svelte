<script lang="ts">
	import { api } from '#lib/api.js';
	import { projectNS, publishGate } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { modules } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Consequence from './Consequence.svelte';
	import StageChip from './StageChip.svelte';
	import StageModal from './StageModal.svelte';

	let { project, onclose }: { project: string; onclose: () => void } = $props();

	const p = $derived(inventory.projects.find((x) => x.name === project));
	const first = $derived(projectNS(p ?? { namespaces: [] }));
	const gate = $derived(publishGate.on(first));
	const c = $derived(caps.ns(first)?.project);
	const host = $derived(`*.${project}.${modules.value?.publishDomain ?? ''}`);
	const reach = $derived(c?.publishingReach ?? []);
	const apart = $derived(
		(p?.namespaces ?? []).map((n) => n.namespace).filter((n) => reach.length && !reach.includes(n)),
	);
	const apartText = $derived(
		apart.length === 1
			? `${apart[0]} has a VM Network of its own: publishing does not reach it.`
			: `${apart.join(', ')} have VM Networks of their own: publishing does not reach them.`,
	);
	// svelte-ignore state_referenced_locally
	const name = project;
</script>

<StageModal
	title={c?.publishingOn ? `Update publishing for ${project}` : `Turn publishing on for ${project}`}
	label="Stage publishing"
	missing={gate.ok ? [] : [gate.why]}
	onsubmit={() => api.publishing(name)}
	onresult={() => ui.toastStaged(`Publishing for ${name} staged in the platform draft.`)}
	{onclose}
>
	<div class="flex items-center gap-2">
		<span class="text-ink-soft">Stages in the platform repo</span>
		<StageChip kind="pr" />
	</div>
	<div class="flex flex-col gap-1.5">
		<Consequence tone="info"
			>{project} gets one Gateway, answering <span class="font-mono">{host}</span> and only routes
			from {project}'s namespaces.</Consequence
		>
		{#if reach.length}
			<Consequence tone="info">Their admins may publish VMs from {reach.join(', ')}.</Consequence>
		{/if}
		{#if apart.length}
			<Consequence>{apartText}</Consequence>
		{/if}
		<Consequence
			>Point <span class="font-mono">{host}</span> at the Gateway's address once it has one.</Consequence
		>
		<Consequence>Nothing changes until the platform pull request merges.</Consequence>
	</div>
</StageModal>
