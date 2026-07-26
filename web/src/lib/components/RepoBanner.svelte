<script lang="ts">
	import { TriangleAlert } from 'lucide-svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';

	// Untracked projects sit collapsed in the tree, hiding its attach/recover
	// buttons exactly when someone clicks INTO the project to see what is wrong.
	// Same gates as the tree; the backend still refuses when the repo resolves.
	let { project }: { project: string } = $props();

	const p = $derived(inventory.inventory?.projects.find((x) => x.name === project));
	const attach = $derived(!!p?.error && !p?.repo);
	// Recovery offers on either error plane: the resolver's (pre-app) or GitOps'.
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
