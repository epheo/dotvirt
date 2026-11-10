<script lang="ts">
	import { Ban, CheckCircle2, LogOut, MoveRight, Wrench } from '@lucide/svelte';
	import { api, type NodeInfo, type VM } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { hostGate } from '#lib/gates.js';
	import { action } from '#lib/resource.svelte.js';
	import { vmsOff } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import Consequence from './Consequence.svelte';
	import StageChip from './StageChip.svelte';

	// A host's maintenance verbs, for its page header. Entering flips the
	// node's maintenance annotation + cordon in one server patch, then one
	// evacuate call sweeps EVERY running VM off the node - the server reads the
	// full snapshot, so VMs outside the caller's projects (invisible in `vms`)
	// move too, while each migrate still runs under the caller's RBAC and lands
	// in the action dock. `vms` is the live inventory stream, so the visible
	// remaining count drains as migrations complete. Plain cordon stays as the
	// lighter verb. info is the host's own read; onchange reads it again.
	let {
		node,
		vms,
		info,
		onchange,
	}: {
		node: string;
		vms: VM[];
		info: NodeInfo;
		onchange: () => Promise<unknown>;
	} = $props();

	const op = action({ toast: ui.toastError }); // one busy flag drives every verb
	const gate = $derived(hostGate(info));
	// Entering maintenance asks once more, in place, with what it will do.
	let armed = $state(false);
	$effect(() => {
		void node;
		armed = false;
	});

	const running = $derived(vms.filter((v) => v.phase === 'Running'));
	// Not yet on the move: retry targets. An active migration would 409 a second one.
	const pending = $derived(
		running.filter((v) => !v.migration || v.migration.completed || v.migration.failed),
	);
	const draining = $derived(info.maintenance && running.length > 0);

	function toggleCordon() {
		const cordon = !info.unschedulable;
		op.run(async () => {
			await api.setNodeCordon(node, cordon);
			await onchange();
			ui.showToast(cordon ? `${node} cordoned. No new VMs land there.` : `${node} uncordoned.`, {
				kind: 'success',
			});
		});
	}

	// One server-side sweep per click: failures are tallied per VM, never
	// aborting the sweep, and cordon already blocks new placements - stragglers
	// get the Retry button.
	async function evacuate(lead: string) {
		const res = await api.evacuateNode(node);
		const skipped = res.skipped ? `, ${res.skipped} already migrating` : '';
		const failed = res.failures.length ? `, ${res.failures.length} failed` : '';
		ui.showToast(
			`${lead}: migration requested for ${plural(res.requested, 'VM')}${skipped}${failed}.`,
			{ kind: res.failures.length ? 'error' : 'success' },
		);
	}

	function enterMaintenance() {
		if (!armed) {
			armed = true;
			return;
		}
		armed = false;
		op.run(async () => {
			await api.setNodeMaintenance(node, true);
			await onchange();
			// Always sweep: the node may hold VMs outside the caller's projects,
			// which the visible `running` count cannot see.
			await evacuate(`${node} entering maintenance`);
		});
	}

	function retryEvacuation() {
		op.run(() => evacuate(`Evacuating ${node}`));
	}

	function exitMaintenance() {
		op.run(async () => {
			await api.setNodeMaintenance(node, false);
			await onchange();
			ui.showToast(`${node} left maintenance. New VMs can land there again.`, {
				kind: 'success',
			});
		});
	}

	const disabled = $derived(op.busy || !gate.ok);
</script>

<div class="flex max-w-[460px] flex-col items-end gap-1.5">
	<div class="flex flex-wrap items-center justify-end gap-2">
		{#if info.maintenance}
			<!-- Always offered: the node may still hold VMs outside the caller's
			     projects, which `pending` cannot count. -->
			<Button
				variant="secondary"
				onclick={retryEvacuation}
				{disabled}
				title={gate.why || 'Live-migrate the VMs still on this host'}
				><MoveRight size={16} />Retry evacuation{pending.length
					? ` (${pending.length})`
					: ''}</Button
			>
			<Button variant="outline" onclick={exitMaintenance} {disabled} title={gate.why}
				><LogOut size={16} />Exit maintenance</Button
			>
		{:else if armed}
			<Button variant="ghost" onclick={() => (armed = false)} disabled={op.busy}>Cancel</Button>
			<Button onclick={enterMaintenance} {disabled}><Wrench size={16} />Confirm maintenance</Button>
		{:else}
			<Button variant="secondary" onclick={toggleCordon} {disabled} title={gate.why}>
				{#if info.unschedulable}<CheckCircle2 size={16} />Uncordon{:else}<Ban
						size={16}
					/>Cordon{/if}
			</Button>
			<Button
				variant="outline"
				onclick={enterMaintenance}
				{disabled}
				title={gate.why || 'Cordon this host and live-migrate every running VM away'}
				><Wrench size={16} />Enter maintenance</Button
			>
		{/if}
		<StageChip kind="now" />
	</div>
	{#if !gate.ok}
		<p class="text-xs leading-4 text-ink-faint">{gate.why}</p>
	{:else if armed && vmsOff()}
		<!-- Evacuation moves VMs only: without KubeVirt nothing here moves. -->
		<Consequence size="sm" tone="warn">
			Cordons {node} until you exit maintenance. Workloads running here stay.
		</Consequence>
	{:else if armed}
		<Consequence size="sm" tone="warn">
			Cordons {node} and live-migrates {running.length
				? `its ${plural(running.length, 'running VM')}`
				: 'every running VM'} to other hosts. VMs outside your projects move too.
		</Consequence>
	{:else if draining}
		<Consequence size="sm" tone="warn">
			{plural(running.length, 'VM')} still here. Live migration needs another host with room.
		</Consequence>
	{:else if info.maintenance}
		<Consequence size="sm">
			Holds until you exit it, even if something else uncordons {node}.
		</Consequence>
	{:else if info.unschedulable}
		<Consequence size="sm"
			>{vmsOff()
				? 'Takes no new workloads; those running here stay.'
				: 'Takes no new VMs; the VMs running here stay.'}</Consequence
		>
	{:else}
		<Consequence size="sm">
			{vmsOff()
				? 'Both stop new workloads. Maintenance holds until you exit it; nothing running here moves.'
				: 'Maintenance moves its running VMs to other hosts. Cordon only stops new ones.'}
		</Consequence>
	{/if}
</div>
