<script lang="ts">
	import { TriangleAlert } from 'lucide-svelte';
	import { api } from '$lib/api';
	import { action } from '$lib/resource.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { authMethods } from '$lib/state/session.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import Banner from './Banner.svelte';

	// One-click SSO finish for admins. The apply runs under the CALLER's token;
	// the API server's RBAC is the gate, and a refusal surfaces legibly. The
	// banner clears on the backend's word, not the click's.
	const auth = authMethods();
	const pending = $derived(!!auth.data?.sso && !!auth.data?.ssoPending);
	const op = action({ toast: ui.toastError });

	function finish() {
		return op.run(async () => {
			await api.finishSSO();
			await auth.refresh();
			ui.showToast('OpenShift SSO is ready: the sign-in button now works for everyone.', {
				kind: 'success',
			});
		});
	}
</script>

{#if pending && inventory.canManage}
	<Banner tone="warn" size="md">
		<TriangleAlert size={16} class="shrink-0" />
		<span>OpenShift SSO is not ready: its OAuthClient is missing or holds an outdated secret.</span>
		<button
			onclick={finish}
			disabled={op.busy}
			class="ml-auto rounded border border-warn/50 bg-panel px-2 py-0.5 text-xs font-medium hover:bg-warn-soft disabled:opacity-50"
		>
			{op.busy ? 'Finishing…' : 'Finish SSO setup'}
		</button>
	</Banner>
{/if}
