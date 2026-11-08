<script lang="ts">
	import { api, Unauthorized, type Network, type VM } from '#lib/api.js';
	import { bulkOutcome } from '#lib/bulk.js';
	import { plural } from '#lib/format.js';
	import { vmGate } from '#lib/gates.js';
	import { networkKey, resolveNIC } from '#lib/networks.js';
	import { networkRef } from '#lib/objects.js';
	import { action } from '#lib/resource.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import StageChip from '#lib/components/StageChip.svelte';

	// Delete a secondary network with its VMs detached in the same go: one NIC
	// removal per attached VM plus the network's own file, so no VM is left
	// naming a network that is gone.
	let { net, vms, onclose }: { net: Network; vms: VM[]; onclose: () => void } = $props();

	const key = $derived(networkKey(net));
	// The interface each VM has on this network.
	const nics = $derived(
		vms.flatMap((v) => {
			const nic = (v.networks ?? []).find((n) => {
				const r = resolveNIC(n, v.namespace, inventory.networks);
				return r && networkKey(r) === key;
			});
			return nic && v.sourceFile ? [{ vm: v, nic: nic.name }] : [];
		}),
	);
	const projects = $derived(new Set(nics.map((x) => inventory.projectOf(x.vm.namespace))));
	// The network stays until every detach stages, so one VM the caller may not
	// change holds the whole delete.
	const blocked = $derived(
		nics.map((x) => vmGate.update(x.vm.namespace)).find((g) => !g.ok)?.why ?? '',
	);
	const op = action({ toast: ui.toastError });

	async function stage() {
		await op.run(async () => {
			const results = await Promise.allSettled(
				nics.map((x) =>
					api.stageEdit(x.vm.namespace, x.vm.name, {
						sourceFile: x.vm.sourceFile,
						removeNetworks: [x.nic],
					}),
				),
			);
			const denied = results.find(
				(r) => r.status === 'rejected' && r.reason instanceof Unauthorized,
			) as PromiseRejectedResult | undefined;
			if (denied) throw denied.reason;
			const out = bulkOutcome(
				'Detached',
				nics.length,
				nics.map((x) => x.vm),
				results,
			);
			if (out.failed) {
				ui.showToast(`${out.msg} ${net.name} is kept until every VM is detached.`, {
					kind: 'error',
				});
				return;
			}
			const ref = networkRef(net);
			await api.deleteObject(ref.resource, ref.namespace, ref.name);
			ui.toastStaged(
				`Delete of ${net.name} staged${nics.length ? `, ${plural(nics.length, 'VM')} detached` : ''}.`,
			);
			onclose();
		});
	}
</script>

<Modal title="Delete {net.name}" danger size="lg" {onclose}>
	<div class="flex flex-col gap-2 p-4">
		<Consequence tone="danger">Removes {net.name} when the pull request merges.</Consequence>
		{#if nics.length}
			<Consequence tone="warn"
				>Detaches {nics.length} VM{nics.length === 1 ? '' : 's'}: each loses its {net.name} NIC at its
				next restart.</Consequence
			>
		{/if}
		{#if projects.size > 1 || (net.scope === 'shared' && nics.length)}
			<Consequence
				>The detaches stage in {projects.size} project{projects.size === 1 ? '' : 's'}; {net.name} itself
				in the platform repository.</Consequence
			>
		{/if}
		{#if blocked}
			<p class="text-[13px] text-warn-ink">{blocked}</p>
		{/if}
		{#if nics.length < vms.length}
			<Consequence tone="warn"
				>{vms.length - nics.length} VM{vms.length - nics.length === 1 ? '' : 's'} on it are not in git:
				detach them by hand.</Consequence
			>
		{/if}
	</div>
	{#snippet footer()}
		<span class="mr-auto"><StageChip kind="pr" /></span>
		<Button variant="ghost" onclick={onclose}>Cancel</Button>
		<Button variant="danger" disabled={op.busy || !!blocked} title={blocked} onclick={stage}
			>Stage the delete</Button
		>
	{/snippet}
</Modal>
