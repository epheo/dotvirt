<script lang="ts">
	import { TriangleAlert } from 'lucide-svelte';
	import { api } from '$lib/api';
	import { friendlyError } from '$lib/format';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';

	// One-click SSO finish for admins. The apply runs under the CALLER's token;
	// the API server's RBAC is the gate, and a refusal surfaces legibly.
	let pending = $state(false);
	let busy = $state(false);
	$effect(() => {
		api
			.authMethods()
			.then((m) => (pending = m.sso && m.ssoPending))
			.catch(() => {});
	});

	async function finish() {
		busy = true;
		try {
			await api.finishSSO();
			pending = false;
			ui.showToast('OpenShift SSO is ready: the sign-in button now works for everyone.', {
				kind: 'success',
			});
		} catch (e) {
			ui.showToast(friendlyError(e), { kind: 'error' });
		} finally {
			busy = false;
		}
	}
</script>

{#if pending && inventory.canManage}
	<div
		class="flex items-center gap-2 border-b border-warn-soft bg-warn-soft/60 px-4 py-2 text-sm text-warn-ink"
	>
		<TriangleAlert size={16} class="shrink-0" />
		<span>OpenShift SSO is enabled but its OAuthClient is not registered yet.</span>
		<button
			onclick={finish}
			disabled={busy}
			class="ml-auto rounded border border-warn/50 bg-panel px-2 py-0.5 text-xs font-medium hover:bg-warn-soft disabled:opacity-50"
		>
			{busy ? 'Finishing…' : 'Finish SSO setup'}
		</button>
	</div>
{/if}
