<script lang="ts">
	import { Search } from '@lucide/svelte';
	import { api, Unauthorized, vmKey, type Network } from '#lib/api.js';
	import { bulkOutcome } from '#lib/bulk.js';
	import { networkAdds } from '#lib/devices.js';
	import { vmEditGate } from '#lib/gates.js';
	import { attachRef, networkKey } from '#lib/networks.js';
	import { action } from '#lib/resource.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import StageChip from '#lib/components/StageChip.svelte';

	// Attach VMs to a secondary network: pick from the VMs that may attach it
	// (its namespace, or the namespaces a shared network reaches) and stage
	// one NIC add per VM into each project's draft.
	let { net, onclose }: { net: Network; onclose: () => void } = $props();

	const where = $derived(
		new Set(net.scope === 'shared' ? (net.namespaces ?? []) : [net.namespace ?? '']),
	);
	const on = $derived(new Set(networking.vmsOn(networkKey(net)).map(vmKey)));
	const candidates = $derived(
		inventory.allVMs.filter((v) => where.has(v.namespace) && !on.has(vmKey(v)) && vmEditGate(v).ok),
	);
	let query = $state('');
	const q = $derived(query.trim().toLowerCase());
	const shown = $derived(candidates.filter((v) => !q || v.name.toLowerCase().includes(q)));
	let picked = $state<Set<string>>(new Set());
	const toggle = (k: string) => {
		const next = new Set(picked);
		if (next.has(k)) next.delete(k);
		else next.add(k);
		picked = next;
	};
	const op = action({ toast: ui.toastError });

	async function stage() {
		const vms = candidates.filter((v) => picked.has(vmKey(v)));
		await op.run(async () => {
			const results = await Promise.allSettled(
				vms.map((v) =>
					api.stageEdit(v.namespace, v.name, {
						sourceFile: v.sourceFile,
						addNetworks: networkAdds(
							[attachRef(net)],
							(v.networks ?? []).filter((n) => !n.implicit).map((n) => n.name),
						),
					}),
				),
			);
			const denied = results.find(
				(r) => r.status === 'rejected' && r.reason instanceof Unauthorized,
			) as PromiseRejectedResult | undefined;
			if (denied) throw denied.reason;
			const { staged, failed, msg } = bulkOutcome(
				`Attached to ${net.name}: staged`,
				vms.length,
				vms,
				results,
			);
			if (staged) ui.toastStaged(msg, { kind: failed ? 'error' : 'success' });
			else ui.showToast(msg, { kind: 'error' });
			if (!failed) onclose();
		});
	}
</script>

<Modal title="Attach VMs to {net.name}" subtitle="Each VM gets a NIC on it." size="lg" {onclose}>
	<div class="flex flex-col gap-3 p-4">
		<label
			class="flex h-8 items-center gap-2 rounded-md border border-ink-faint/80 bg-panel px-2.5 text-ink-muted"
		>
			<Search size={16} />
			<input
				type="text"
				bind:value={query}
				placeholder="Find a VM"
				aria-label="Find a VM"
				class="min-w-0 flex-1 border-0 bg-transparent text-sm text-ink outline-0"
				data-autofocus
			/>
		</label>
		<div class="flex items-center justify-between text-xs text-ink-faint">
			<span>
				{shown.length
					? `Showing 1-${Math.min(50, shown.length)} of ${shown.length}`
					: 'No VM can attach it.'}
			</span>
			{#if shown.length}
				<button
					type="button"
					class="text-accent-ink hover:underline"
					onclick={() => (picked = new Set(shown.map(vmKey)))}>Select all {shown.length}</button
				>
			{/if}
		</div>
		<ul class="max-h-72 overflow-y-auto rounded-md border border-line">
			{#each shown.slice(0, 50) as v (vmKey(v))}
				<li class="border-b border-line-soft last:border-b-0">
					<label class="flex items-center gap-2 px-3 py-1.5 text-[13px] hover:bg-select-soft">
						<input
							type="checkbox"
							checked={picked.has(vmKey(v))}
							onchange={() => toggle(vmKey(v))}
						/>
						<span class="font-mono font-medium">{v.name}</span>
						<span class="ml-auto text-xs text-ink-faint">{v.namespace}</span>
					</label>
				</li>
			{/each}
		</ul>
		<Consequence tone="info">Each VM gets a {net.name} NIC at its next restart.</Consequence>
		<Consequence>One staged edit per VM, in its project's pull request.</Consequence>
	</div>
	{#snippet footer()}
		<span class="mr-auto"><StageChip kind="pr" /></span>
		<Button variant="ghost" onclick={onclose}>Cancel</Button>
		<Button disabled={!picked.size || op.busy} onclick={stage}
			>Stage {picked.size} attachment{picked.size === 1 ? '' : 's'}</Button
		>
	{/snippet}
</Modal>
