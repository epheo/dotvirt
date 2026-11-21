<script lang="ts">
	import { TriangleAlert } from '@lucide/svelte';
	import { adoptNamespaces } from '#lib/actions.js';
	import { repoGate } from '#lib/gates.js';
	import { lostToGit, opFailed, repoError } from '#lib/gitops.js';
	import { action } from '#lib/resource.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Banner from './Banner.svelte';

	// Untracked projects sit collapsed in the tree, hiding its attach/recover
	// buttons exactly when someone clicks INTO the project to see what is wrong.
	// Same gates as the tree; the backend still refuses when the repo resolves.
	let { project }: { project: string } = $props();

	const p = $derived(inventory.projects.find((x) => x.name === project));
	const attach = $derived(!!p?.error && !p?.repo);
	// A failed sync OPERATION is a manifest problem (the last merged change did
	// not apply), not a repo problem: offering Recover repo there invites
	// re-creating a healthy repo. Recovery stays gated on the resolver's error or
	// GitOps' comparison errors (unreachable/lost repo), which run no operation.
	const syncFailed = $derived(opFailed(p?.gitOps));
	const recover = $derived(!!p && !!p.repo && (!!p.error || !!repoError(p?.gitOps)));
	const note = $derived(p?.error || p?.gitOps?.syncError || '');
	const may = $derived(!!p && repoGate(p).ok);
	// Git lost what runs (a re-created or rewritten repo): ArgoCD holds it, and
	// adopting the namespaces back is the way out, where the caller may stage.
	const lost = $derived(lostToGit(p?.gitOps));
	const adoptable = $derived(
		(p?.namespaces ?? []).map((n) => n.namespace).filter((ns) => inventory.canAuthorVMs(ns)),
	);
	const adoptOp = action({ toast: ui.toastError });

	function open(rec: boolean) {
		if (!p) return;
		ui.modal = {
			kind: 'adoptProject',
			project: p.name,
			namespaces: p.namespaces.map((n) => n.namespace),
			recover: rec,
		};
	}
</script>

{#if (attach || recover) && may}
	<Banner tone="warn">
		<TriangleAlert size={14} class="shrink-0" />
		<span class="truncate" title={note}>
			{#if attach}
				This project has no git repo: its objects are not under GitOps.
			{:else}
				This project's repo is not reachable: {note.slice(0, 100)}
			{/if}
		</span>
		<button
			onclick={() => open(recover)}
			class="ml-auto shrink-0 font-medium text-warn-ink hover:underline"
		>
			{attach ? 'Attach repo' : 'Recover repo'}
		</button>
	</Banner>
{:else if lost}
	<Banner tone="warn">
		<TriangleAlert size={14} class="shrink-0" />
		<span class="truncate">
			Git no longer declares what this project runs: its repo was re-created or rewritten. ArgoCD
			keeps it running until it is adopted into git.
		</span>
		{#if adoptable.length}
			<button
				onclick={() => adoptOp.run(() => adoptNamespaces(adoptable))}
				disabled={adoptOp.busy}
				class="ml-auto shrink-0 font-medium text-warn-ink hover:underline"
			>
				{adoptOp.busy ? 'Capturing…' : 'Adopt into git'}
			</button>
		{/if}
	</Banner>
{:else if syncFailed}
	<Banner tone="danger">
		<TriangleAlert size={14} class="shrink-0" />
		<span class="truncate" title={note}>
			The last sync failed to apply: {note.slice(0, 160)}
		</span>
		<span class="ml-auto shrink-0 text-danger-ink/80">Fix with a new PR or revert the merge.</span>
	</Banner>
{/if}
