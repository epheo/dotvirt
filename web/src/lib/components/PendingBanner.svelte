<script lang="ts">
	import type { VM } from '$lib/api';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import Banner from './Banner.svelte';
	import StatusDot from './StatusDot.svelte';

	// Pending-change awareness on object pages: an unproposed staged change (this
	// user's draft) or an open PR in the object's project. Purely derived from
	// the drafts summary + the live proposals - no extra fetch.
	let { vm = undefined, project = undefined }: { vm?: VM; project?: string } = $props();

	const proj = $derived(vm ? inventory.projectOf(vm.namespace) : (project ?? ''));
	const stagedItem = $derived(
		vm ? drafts.stagedByKey.get(`${vm.namespace}/${vm.name}`) : undefined,
	);
	const stagedCount = $derived(
		!vm && project ? (drafts.drafts.find((d) => d.project === project)?.draft.count ?? 0) : 0,
	);
	const proposal = $derived(proj ? inventory.proposals.find((p) => p.project === proj) : undefined);
</script>

{#if stagedItem || stagedCount}
	<Banner tone="accent">
		<StatusDot tone="info" size="xs" />
		{#if stagedItem}
			A staged <strong>{stagedItem.kind}</strong> for this VM is waiting in Changes — not yet proposed.
		{:else}
			{stagedCount} staged change{stagedCount === 1 ? '' : 's'} in this project — not yet proposed.
		{/if}
		<button
			onclick={() => (ui.changesOpen = true)}
			class="font-medium text-accent-ink hover:underline"
		>
			Review &amp; propose
		</button>
	</Banner>
{:else if proposal}
	<Banner tone="ok">
		<StatusDot tone="ok" size="xs" />
		PR #{proposal.prNumber} is open in <strong>{proj}</strong> — its changes apply when it merges.
		<a href={proposal.prURL} target="_blank" rel="noopener" class="font-medium hover:underline"
			>View PR ↗</a
		>
	</Banner>
{/if}
