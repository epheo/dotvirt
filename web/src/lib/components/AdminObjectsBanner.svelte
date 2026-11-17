<script lang="ts">
	import { ShieldAlert } from '@lucide/svelte';
	import { api } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Banner from './Banner.svelte';

	// What the install needs but its operator may not create, as the console
	// renders it, installed with one click under the cluster admin's own token.
	// Read only once the capability says the caller may install it all; the
	// banner clears on the server's word, not the click's. Each object says
	// what differs, so installing over another admin's change is a choice.
	const allowed = $derived(clusterGate.adminObjects().ok);
	const objects = resource(
		() => (allowed ? 'admin-objects' : ''),
		(key) => (key ? api.adminObjects() : Promise.resolve(null)),
	);
	const pending = $derived((objects.data?.objects ?? []).filter((o) => o.state !== 'Installed'));
	const op = action({ toast: ui.toastError });

	// The same render as a file, for an admin who applies it with kubectl: the
	// command takes no data, so nothing passes through a shell. A stale object
	// is to remove, not to apply: it is not in the file.
	const FILE = 'dotvirt-admin-objects.json';
	const COMMAND = `kubectl apply --server-side --field-manager=dotvirt -f ${FILE}`;
	function download() {
		const items = (objects.data?.objects ?? []).flatMap((o) => (o.object ? [o.object] : []));
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

{#if allowed && objects.data && pending.length}
	{@const d = objects.data}
	<Banner tone="warn" size="md" class="items-start">
		<ShieldAlert size={16} class="mt-0.5 shrink-0" />
		<span class="flex flex-col gap-1">
			<span>This install needs a cluster admin to install what its operator may not:</span>
			<ul class="list-disc pl-5" data-testid="admin-objects">
				{#each pending as o (o.kind + '/' + o.name)}
					<li>
						{o.purpose}
						<span class="text-xs"
							>({o.state === 'Outdated'
								? `differs: ${o.detail}`
								: o.state === 'Stale'
									? `stale, ${o.detail}`
									: 'missing'})</span
						>
					</li>
				{/each}
			</ul>
			<span class="text-xs" data-testid="admin-pins">
				Pinned to {d.operator}, namespace {d.namespace}, ArgoCD {d.argoNamespace} ({d.argoController}){d
					.images.length
					? `, images ${d.images.join(', ')}`
					: ''}.
			</span>
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
