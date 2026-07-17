<script lang="ts">
	import { untrack } from 'svelte';
	import { Ban, CheckCircle2, LogOut, MoveRight, Wrench } from 'lucide-svelte';
	import { api, Unauthorized, type NodeInfo, type VM } from '$lib/api';

	// Host maintenance (vCenter's Enter/Exit Maintenance Mode): entering flips
	// the node's maintenance annotation + cordon in one server patch, then this
	// client drives one migrate call per running VM — so each move is gated by
	// that VM's own RBAC and lands in the action dock. Progress needs no
	// polling: `vms` is the live inventory stream, so the remaining count
	// drains as migrations complete. Plain cordon stays as the lighter verb.
	// Hidden unless the token may patch nodes.
	let {
		node,
		vms,
		onaction,
	}: {
		node: string;
		vms: VM[];
		onaction?: (a: { verb: string; namespace: string; name: string; ok: boolean }) => void;
	} = $props();

	let info = $state<NodeInfo | null>(null);
	let busy = $state(false);
	let confirming = $state(false);
	let msg = $state('');
	let ok = $state(true);

	const running = $derived(vms.filter((v) => v.phase === 'Running'));
	// Not yet on the move: retry targets. An active migration would 409 a second one.
	const pending = $derived(
		running.filter((v) => !v.migration || v.migration.completed || v.migration.failed),
	);
	const entering = $derived(!!info?.maintenance && running.length > 0);

	async function load() {
		try {
			info = await api.nodeInfo(node);
		} catch (e) {
			if (e instanceof Unauthorized) return;
			info = null; // no node-read RBAC → panel stays hidden
		}
	}
	$effect(() => {
		node;
		untrack(load);
	});

	async function toggleCordon() {
		if (!info) return;
		busy = true;
		msg = '';
		try {
			await api.setNodeCordon(node, !info.unschedulable);
			await load();
			msg = info?.unschedulable ? 'Node cordoned — no new placements.' : 'Node uncordoned.';
			ok = true;
		} catch (e) {
			if (e instanceof Unauthorized) return;
			msg = String(e);
			ok = false;
		} finally {
			busy = false;
		}
	}

	// One migrate call per pending VM; failures are tallied, never aborting the
	// sweep. Cordon already blocks new placements, so one sweep per click is
	// enough — stragglers get the Retry button.
	async function evacuate(): Promise<string> {
		let migrated = 0;
		let failed = 0;
		for (const vm of pending) {
			try {
				await api.migrate(vm.namespace, vm.name);
				migrated++;
				onaction?.({ verb: 'Live-migration', namespace: vm.namespace, name: vm.name, ok: true });
			} catch (e) {
				if (e instanceof Unauthorized) return '';
				failed++;
				onaction?.({ verb: 'Live-migration', namespace: vm.namespace, name: vm.name, ok: false });
			}
		}
		ok = failed === 0;
		return `migration requested for ${migrated} VM${migrated === 1 ? '' : 's'}${failed ? `, ${failed} failed` : ''}`;
	}

	async function enterMaintenance() {
		confirming = false;
		busy = true;
		msg = '';
		try {
			await api.setNodeMaintenance(node, true);
			await load();
			const sweep = running.length ? ` — ${await evacuate()}` : '';
			msg = `Entering maintenance mode${sweep}.`;
		} catch (e) {
			if (e instanceof Unauthorized) return;
			msg = String(e);
			ok = false;
		} finally {
			busy = false;
		}
	}

	async function retryEvacuation() {
		busy = true;
		msg = '';
		msg = `Evacuation ${await evacuate()} — watch the migrations in the dock.`;
		busy = false;
	}

	async function exitMaintenance() {
		busy = true;
		msg = '';
		try {
			await api.setNodeMaintenance(node, false);
			await load();
			msg = 'Maintenance mode exited — node is schedulable again.';
			ok = true;
		} catch (e) {
			if (e instanceof Unauthorized) return;
			msg = String(e);
			ok = false;
		} finally {
			busy = false;
		}
	}
</script>

{#if info?.canCordon}
	<section class="max-w-2xl rounded border border-line">
		<h3
			class="border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
		>
			Maintenance
		</h3>
		<div class="space-y-3 p-3">
			<div class="flex items-center gap-2 text-sm">
				<span class="text-ink-muted">Status:</span>
				{#if entering}
					<span class="inline-flex items-center gap-1.5 font-medium text-warn-ink">
						<Wrench size={14} /> Entering maintenance — {running.length} VM{running.length === 1
							? ''
							: 's'} still here
					</span>
				{:else if info.maintenance}
					<span class="inline-flex items-center gap-1.5 font-medium text-warn-ink">
						<Wrench size={14} /> Maintenance mode
					</span>
				{:else if info.unschedulable}
					<span class="inline-flex items-center gap-1.5 font-medium text-warn-ink">
						<Ban size={14} /> Cordoned
					</span>
				{:else}
					<span class="inline-flex items-center gap-1.5 font-medium text-ok-ink">
						<CheckCircle2 size={14} /> Schedulable
					</span>
				{/if}
			</div>
			{#if confirming}
				<div class="space-y-2 rounded border border-line bg-inset p-2.5">
					<p class="text-xs text-ink-soft">
						Cordon <span class="font-mono">{node}</span>
						{#if running.length}
							and live-migrate its {running.length} running VM{running.length === 1 ? '' : 's'} to other
							hosts?
						{:else}
							? It has no running VMs.
						{/if}
					</p>
					<div class="flex items-center gap-2">
						<button
							onclick={enterMaintenance}
							disabled={busy}
							class="rounded bg-accent px-2.5 py-1 text-xs font-medium text-white disabled:opacity-50"
						>
							Enter Maintenance Mode
						</button>
						<button
							onclick={() => (confirming = false)}
							disabled={busy}
							class="rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							Cancel
						</button>
					</div>
				</div>
			{:else}
				<div class="flex flex-wrap items-center gap-2">
					{#if info.maintenance}
						<button
							onclick={exitMaintenance}
							disabled={busy}
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							<LogOut size={13} /> Exit Maintenance Mode
						</button>
						{#if pending.length}
							<button
								onclick={retryEvacuation}
								disabled={busy}
								title="Live-migrate the VMs still on this node"
								class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
							>
								<MoveRight size={13} /> Retry evacuation ({pending.length})
							</button>
						{/if}
					{:else}
						<button
							onclick={() => (confirming = true)}
							disabled={busy}
							title="Cordon this node and live-migrate every running VM away"
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							<Wrench size={13} /> Enter Maintenance Mode
						</button>
						<button
							onclick={toggleCordon}
							disabled={busy}
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							{#if info.unschedulable}<CheckCircle2 size={13} /> Uncordon{:else}<Ban size={13} /> Cordon{/if}
						</button>
					{/if}
				</div>
			{/if}
			<p class="text-xs text-ink-faint">
				{#if info.maintenance}
					Maintenance holds until you exit it, even if the node is uncordoned elsewhere.
					Live-migration needs another schedulable host with capacity.
				{:else}
					Maintenance mode cordons the node and live-migrates its running VMs away. Cordon alone
					stops new placements; running VMs stay.
				{/if}
			</p>
			{#if msg}
				<p class="text-xs {ok ? 'text-ink-soft' : 'text-danger-ink'}">{msg}</p>
			{/if}
		</div>
	</section>
{/if}
