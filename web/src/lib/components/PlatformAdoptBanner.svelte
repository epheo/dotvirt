<script lang="ts">
	import { GitPullRequest } from 'lucide-svelte';
	import { api, Unauthorized } from '$lib/api';
	import { friendlyError } from '$lib/format';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import Banner from './Banner.svelte';

	// The platform tier's AdoptBanner: shared segments and cluster-wide policies
	// running with no manifest in the platform repo. Shown only to callers who may
	// author that tier, where they already are (the Networking root).
	const untracked = $derived(
		inventory.networks.filter((n) => n.scope === 'shared' && !n.sourceFile).length +
			inventory.policies.filter((p) => !p.namespace && !p.sourceFile).length,
	);
	let busy = $state(false);
	async function adopt() {
		if (busy) return;
		busy = true;
		try {
			const view = await api.adoptPlatform();
			await drafts.refresh();
			ui.showToast(
				[`${view.count} platform objects staged into Changes.`, view.warning]
					.filter(Boolean)
					.join(' '),
				{
					kind: 'success',
					action: { label: 'Review & propose', run: () => ui.openChanges() },
				},
			);
		} catch (e) {
			if (!(e instanceof Unauthorized)) ui.showToast(friendlyError(e), { kind: 'error' });
		} finally {
			busy = false;
		}
	}
</script>

{#if inventory.canManage && untracked > 0}
	<Banner tone="accent">
		<GitPullRequest size={14} class="shrink-0" />
		<span class="min-w-0 truncate">
			<strong>{untracked} shared {untracked === 1 ? 'object runs' : 'objects run'}</strong>
			with no manifest in the platform repository, so GitOps does not manage {untracked === 1
				? 'it'
				: 'them'}. Adopting captures every untracked shared segment, VLAN, admin policy, egress IP,
			external route and uplink into one pull request; nothing changes until it merges.
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
