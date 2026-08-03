<script lang="ts">
	import { GitPullRequest } from 'lucide-svelte';
	import { adoptNamespaces } from '$lib/actions';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import Banner from './Banner.svelte';

	// Brownfield guidance: a repo-backed project running objects git does not
	// describe gets the adopt call-to-action where the user already is, not only
	// in the tree's right-click menu. RepoBanner owns the broken-repo states
	// (attach/recover), so this shows only when the repo is healthy and adoption
	// would actually stage something.
	let { project, namespace }: { project: string; namespace?: string } = $props();

	const p = $derived(inventory.inventory?.projects.find((x) => x.name === project));
	const healthy = $derived(!!p?.repo && !p?.error && !p?.gitOps?.syncError);
	const scoped = $derived(namespace ? [namespace] : (p?.namespaces.map((n) => n.namespace) ?? []));
	// The inventory can only see git not describing VMs; the capture itself sweeps
	// the whole namespace (networks, policies, disks), so the copy says so.
	const untracked = $derived.by(() => {
		const want = new Set(scoped);
		return inventory.allVMs.filter((v) => want.has(v.namespace) && v.sync === 'NotTracked');
	});

	let busy = $state(false);
	async function adopt() {
		if (busy) return;
		busy = true;
		try {
			await adoptNamespaces(new Set(untracked.map((v) => v.namespace)), {
				onstaged: () => drafts.refresh(),
			});
		} finally {
			busy = false;
		}
	}
</script>

{#if healthy && untracked.length}
	<Banner tone="accent">
		<GitPullRequest size={14} class="shrink-0" />
		<span class="min-w-0 truncate">
			<strong>{untracked.length} VM{untracked.length === 1 ? '' : 's'}</strong>
			{untracked.length === 1 ? 'runs' : 'run'} in this {namespace ? 'namespace' : 'project'} but {untracked.length ===
			1
				? 'is'
				: 'are'} not described in git, so GitOps does not manage {untracked.length === 1
				? 'it'
				: 'them'}. Adopting captures the live manifests of everything untracked here (VMs, networks,
			policies) into one pull request; nothing changes until it merges.
		</span>
		<button
			onclick={adopt}
			disabled={busy}
			class="ml-auto shrink-0 font-medium text-accent-ink hover:underline disabled:opacity-50"
		>
			{busy ? 'Capturing…' : 'Adopt into git'}
		</button>
	</Banner>
{/if}
