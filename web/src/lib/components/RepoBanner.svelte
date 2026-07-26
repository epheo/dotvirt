<script lang="ts">
	import { TriangleAlert } from 'lucide-svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';

	// Repo-state awareness on the project's own summary: untracked projects sit
	// COLLAPSED in the tree by default, so the tree-side "Attach repo"/"Recover
	// repo" affordances are invisible exactly when someone clicks INTO the project
	// to see what's wrong. Same gates as the tree: resolver error = attach,
	// GitOps error on a repo-backed project = recover (the backend still refuses
	// when the repo resolves, so a transient sync error ends in a clear conflict).
	let { project }: { project: string } = $props();

	const p = $derived(inventory.inventory?.projects.find((x) => x.name === project));
	const attach = $derived(!!p?.error && !p?.repo);
	// A repo-backed project is offered recovery on EITHER error plane: the resolver's
	// ("repo unavailable", set before any Argo app exists) or the GitOps rollup's.
	const recover = $derived(!!p && !!p.repo && (!!p.error || !!p.gitOps?.syncError));
	const note = $derived(p?.error || p?.gitOps?.syncError || '');

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

{#if (attach || recover) && inventory.canManage}
	<div
		class="flex items-center gap-2 border-b border-warn-soft bg-warn-soft/60 px-4 py-1.5 text-xs text-warn-ink"
	>
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
	</div>
{/if}
