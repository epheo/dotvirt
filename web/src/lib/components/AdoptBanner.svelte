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
	// Untracked VMs, project networks and rules in the scope; the capture itself
	// sweeps the whole namespace (disks too), so the copy says so.
	const untrackedVMs = $derived.by(() => {
		const want = new Set(scoped);
		return inventory.allVMs.filter((v) => want.has(v.namespace) && v.sync === 'NotTracked');
	});
	const untrackedObjects = $derived.by(() => {
		const want = new Set(scoped);
		return (
			inventory.networks.filter(
				(n) => n.scope === 'project' && want.has(n.namespace ?? '') && !n.sourceFile,
			).length +
			inventory.policies.filter((p) => !!p.namespace && want.has(p.namespace) && !p.sourceFile)
				.length
		);
	});
	const untracked = $derived(untrackedVMs.length + untrackedObjects);
	// One string, so the sentence never wraps in the markup: locators match it whole.
	const copy = $derived(
		`in this ${namespace ? 'namespace' : 'project'} but ${untracked === 1 ? 'is' : 'are'} not described in git, so GitOps does not manage ${untracked === 1 ? 'it' : 'them'}. Adopting captures the live manifests of everything untracked here (VMs, networks, policies, disks) into one pull request; nothing changes until it merges.`,
	);
	const adoptNS = $derived.by(() => {
		const want = new Set(scoped);
		const nss = new Set(untrackedVMs.map((v) => v.namespace));
		for (const n of inventory.networks)
			if (n.scope === 'project' && !n.sourceFile && want.has(n.namespace ?? ''))
				nss.add(n.namespace!);
		for (const p of inventory.policies)
			if (p.namespace && !p.sourceFile && want.has(p.namespace)) nss.add(p.namespace);
		return nss;
	});

	let busy = $state(false);
	async function adopt() {
		if (busy) return;
		busy = true;
		try {
			await adoptNamespaces(adoptNS, {
				onstaged: () => drafts.refresh(),
			});
		} finally {
			busy = false;
		}
	}
</script>

{#if healthy && untracked > 0}
	<Banner tone="accent">
		<GitPullRequest size={14} class="shrink-0" />
		<span class="min-w-0 truncate"
			><strong>{untracked} {untracked === 1 ? 'object runs' : 'objects run'}</strong> {copy}</span
		>
		<button
			onclick={adopt}
			disabled={busy}
			class="ml-auto shrink-0 font-medium text-accent-ink hover:underline disabled:opacity-50"
		>
			{busy ? 'Capturing…' : 'Adopt into git'}
		</button>
	</Banner>
{/if}
