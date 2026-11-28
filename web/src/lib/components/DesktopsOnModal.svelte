<script lang="ts">
	import { api } from '#lib/api.js';
	import { desktopsGate, projectNS } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Consequence from './Consequence.svelte';
	import StageChip from './StageChip.svelte';
	import StageModal from './StageModal.svelte';

	let { project, namespace, onclose }: { project: string; namespace: string; onclose: () => void } =
		$props();

	const p = $derived(inventory.projects.find((x) => x.name === project));
	const first = $derived(projectNS(p ?? { namespaces: [] }));
	const gate = $derived(desktopsGate.on(first, namespace));
	const on = $derived(!!caps.ns(first)?.project?.desktopsOn?.includes(namespace));
	// svelte-ignore state_referenced_locally
	const [name, ns] = [project, namespace];
</script>

<StageModal
	title={on ? `Update desktops in ${namespace}` : `Turn desktops on in ${namespace}`}
	label="Stage desktops"
	missing={gate.ok ? [] : [gate.why]}
	onsubmit={() => api.desktopsOn(name, ns)}
	onresult={() => ui.toastStaged(`Desktops in ${ns} staged in the platform draft.`)}
	{onclose}
>
	<div class="flex items-center gap-2">
		<span class="text-ink-soft">Stages in the platform repo</span>
		<StageChip kind="pr" />
	</div>
	<div class="flex flex-col gap-1.5">
		<Consequence tone="info">
			{project}'s admins and its repo may declare desktop pools and desktops in
			<span class="font-mono">{namespace}</span>.
		</Consequence>
		<Consequence tone="info">
			Its admins may start, stop and release anyone's desktop there.
		</Consequence>
		<Consequence tone="info">
			Its authors may read the desktops there. Its viewers may not: a desktop carries a pending
			pairing PIN.
		</Consequence>
		<Consequence>Nothing changes until the platform pull request merges.</Consequence>
	</div>
</StageModal>
