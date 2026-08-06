<script lang="ts">
	import { Ban, CheckCircle2, LogOut, MoveRight, Wrench } from 'lucide-svelte';
	import { api, type NodeInfo, type VM } from '$lib/api';
	import { action, resource } from '$lib/resource.svelte';

	// Host maintenance: entering flips
	// the node's maintenance annotation + cordon in one server patch, then one
	// evacuate call sweeps EVERY running VM off the node - the server reads the
	// full snapshot, so VMs outside the caller's projects (invisible in `vms`)
	// move too, while each migrate still runs under the caller's RBAC and lands
	// in the action dock. `vms` is the live inventory stream, so the visible
	// remaining count drains as migrations complete. Plain cordon stays as the
	// lighter verb. Hidden unless the token may patch nodes.
	let {
		node,
		vms,
	}: {
		node: string;
		vms: VM[];
	} = $props();

	const op = action(); // one busy/error pair drives every maintenance verb
	let confirming = $state(false);
	let msg = $state(''); // success/summary line; op.error carries failures
	let ok = $state(true); // false when an evacuation sweep had failures

	const running = $derived(vms.filter((v) => v.phase === 'Running'));
	// Not yet on the move: retry targets. An active migration would 409 a second one.
	const pending = $derived(
		running.filter((v) => !v.migration || v.migration.completed || v.migration.failed),
	);

	// no node-read RBAC -> panel stays hidden (failed maps to null)
	const infoRes = resource<NodeInfo>(
		() => node,
		() => api.nodeInfo(node),
		{ reset: true },
	);
	const info = $derived(infoRes.failed ? null : infoRes.data);
	const entering = $derived(!!info?.maintenance && running.length > 0);

	async function toggleCordon() {
		if (!info) return;
		const cordon = !info.unschedulable;
		msg = '';
		if (
			await op.run(async () => {
				await api.setNodeCordon(node, cordon);
				await infoRes.refresh();
			})
		) {
			msg = info?.unschedulable ? 'Node cordoned — no new placements.' : 'Node uncordoned.';
			ok = true;
		}
	}

	// One server-side sweep per click: failures are tallied per VM, never
	// aborting the sweep, and cordon already blocks new placements - stragglers
	// get the Retry button.
	async function evacuate(): Promise<string> {
		const res = await api.evacuateNode(node);
		ok = res.failures.length === 0;
		const skipped = res.skipped ? `, ${res.skipped} already migrating` : '';
		const failed = res.failures.length ? `, ${res.failures.length} failed` : '';
		return `migration requested for ${res.requested} VM${res.requested === 1 ? '' : 's'}${skipped}${failed}`;
	}

	async function enterMaintenance() {
		confirming = false;
		msg = '';
		await op.run(async () => {
			await api.setNodeMaintenance(node, true);
			await infoRes.refresh();
			// Always sweep: the node may hold VMs outside the caller's projects,
			// which the visible `running` count cannot see.
			msg = `Entering maintenance mode — ${await evacuate()}.`;
		});
	}

	async function retryEvacuation() {
		msg = '';
		await op.run(async () => {
			msg = `Evacuation ${await evacuate()} — watch the migrations in the dock.`;
		});
	}

	async function exitMaintenance() {
		msg = '';
		if (
			await op.run(async () => {
				await api.setNodeMaintenance(node, false);
				await infoRes.refresh();
			})
		) {
			msg = 'Maintenance mode exited — node is schedulable again.';
			ok = true;
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
						Cordon <span class="font-mono">{node}</span> and live-migrate every running VM to other
						hosts?
						{#if running.length}
							{running.length} of your VM{running.length === 1 ? ' is' : 's are'} here; VMs outside your
							projects move too.
						{:else}
							None of your VMs are here, but VMs outside your projects move too.
						{/if}
					</p>
					<div class="flex items-center gap-2">
						<button
							onclick={enterMaintenance}
							disabled={op.busy}
							class="rounded bg-accent px-2.5 py-1 text-xs font-medium text-white disabled:opacity-50"
						>
							Enter Maintenance Mode
						</button>
						<button
							onclick={() => (confirming = false)}
							disabled={op.busy}
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
							disabled={op.busy}
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							<LogOut size={13} /> Exit Maintenance Mode
						</button>
						<!-- Always offered: the node may still hold VMs outside the caller's
						     projects, which `pending` cannot count. -->
						<button
							onclick={retryEvacuation}
							disabled={op.busy}
							title="Live-migrate the VMs still on this node"
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							<MoveRight size={13} /> Retry evacuation{pending.length ? ` (${pending.length})` : ''}
						</button>
					{:else}
						<button
							onclick={() => (confirming = true)}
							disabled={op.busy}
							title="Cordon this node and live-migrate every running VM away"
							class="flex items-center gap-1.5 rounded border border-line-strong px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
						>
							<Wrench size={13} /> Enter Maintenance Mode
						</button>
						<button
							onclick={toggleCordon}
							disabled={op.busy}
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
			{#if op.error}
				<p class="text-xs text-danger-ink">{op.error}</p>
			{:else if msg}
				<p class="text-xs {ok ? 'text-ink-soft' : 'text-danger-ink'}">{msg}</p>
			{/if}
		</div>
	</section>
{/if}
