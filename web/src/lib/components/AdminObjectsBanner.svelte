<script lang="ts">
	import { ShieldAlert } from '@lucide/svelte';
	import { api } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Banner from './Banner.svelte';

	// What the install needs but its operator may not create, installed with
	// one click under the cluster admin's own token. Read only once the
	// capability says the caller may install it all; the banner clears on the
	// server's word, not the click's.
	const allowed = $derived(clusterGate.adminObjects().ok);
	const objects = resource(
		() => (allowed ? 'admin-objects' : ''),
		(key) => (key ? api.adminObjects() : Promise.resolve(null)),
	);
	const pending = $derived((objects.data?.objects ?? []).filter((o) => o.state !== 'Installed'));
	const purposes = $derived([...new Set(pending.map((o) => o.purpose))]);
	const op = action({ toast: ui.toastError });

	// The same objects as a file, for an admin who applies them with kubectl:
	// the command takes no data, so nothing passes through a shell.
	const FILE = 'dotvirt-admin-objects.json';
	const COMMAND = `kubectl apply --server-side --field-manager=dotvirt -f ${FILE}`;
	function download() {
		const items = (objects.data?.objects ?? []).map((o) => o.object);
		const blob = new Blob([JSON.stringify({ apiVersion: 'v1', kind: 'List', items }, null, 2)], {
			type: 'application/json',
		});
		const a = document.createElement('a');
		a.href = URL.createObjectURL(blob);
		a.download = FILE;
		a.click();
		URL.revokeObjectURL(a.href);
	}

	function install() {
		return op.run(async () => {
			await api.installAdminObjects();
			await objects.refresh();
			ui.showToast('Installed what the install asked a cluster admin for.', { kind: 'success' });
		});
	}
</script>

{#if allowed && pending.length}
	<Banner tone="warn" size="md" class="items-start">
		<ShieldAlert size={16} class="mt-0.5 shrink-0" />
		<span>
			This install needs a cluster admin to install what its operator may not:
			<ul class="list-disc pl-5" data-testid="admin-objects">
				{#each purposes as p (p)}<li>{p}</li>{/each}
			</ul>
			<span class="text-xs">
				Or download the manifest and apply it: <code class="font-mono">{COMMAND}</code>
			</span>
		</span>
		<span class="ml-auto flex shrink-0 gap-2">
			<button
				onclick={download}
				class="rounded border border-warn/50 bg-panel px-2 py-0.5 text-xs font-medium hover:bg-warn-soft"
			>
				Download manifest
			</button>
			<button
				onclick={install}
				disabled={op.busy}
				class="rounded border border-warn/50 bg-panel px-2 py-0.5 text-xs font-medium hover:bg-warn-soft disabled:opacity-50"
			>
				{op.busy ? 'Installing…' : 'Install'}
			</button>
		</span>
	</Banner>
{/if}
