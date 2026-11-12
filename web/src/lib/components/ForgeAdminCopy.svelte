<script lang="ts">
	import { api } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { action } from '#lib/resource.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';

	// Offered only to callers the server's check says may read the managed
	// forge's admin Secret. The read runs under their own token on the click,
	// and the password goes to the clipboard alone: never into the page or
	// any state.
	let { class: cls = '' }: { class?: string } = $props();
	const op = action({ toast: ui.toastError });

	function copy() {
		let user = '';
		return op.run(async () => {
			const text = api.forgeAdminPassword().then((c) => {
				user = c.username;
				return c.password;
			});
			// Safari keeps the click's permission only for a write started in
			// it, so the pending read rides the clipboard item.
			if (typeof ClipboardItem !== 'undefined') {
				const blob = text.then((t) => new Blob([t], { type: 'text/plain' }));
				await Promise.all([
					navigator.clipboard.write([new ClipboardItem({ 'text/plain': blob })]),
					text,
				]);
			} else {
				await navigator.clipboard.writeText(await text);
			}
			ui.showToast(`Forge admin password copied. Sign in to the forge as ${user}.`, {
				kind: 'success',
			});
		});
	}
</script>

{#if clusterGate.forgeAdmin().ok}
	<button class="text-ink-soft hover:underline {cls}" onclick={copy} disabled={op.busy}
		>{op.busy ? 'Copying...' : 'Copy the forge admin password'}</button
	>
{/if}
