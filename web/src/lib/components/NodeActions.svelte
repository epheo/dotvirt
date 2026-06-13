<script lang="ts">
	import { untrack } from 'svelte';
	import { Ban, CheckCircle2, MoveRight } from 'lucide-svelte';
	import { api, Unauthorized, type NodeInfo, type VM } from '$lib/api';

	// Node maintenance-lite (vCenter's host maintenance, minus full drain):
	// cordon/uncordon stops new placements, Evacuate live-migrates the node's
	// running VMs away. Cordon patches node.spec.unschedulable under the user's
	// token; Evacuate reuses the per-VM migrate (so its RBAC + the live
	// migration rows in the dock apply). Hidden unless the token may cordon.
	let {
		node,
		vms,
		onaction
	}: {
		node: string;
		vms: VM[];
		onaction?: (a: { verb: string; namespace: string; name: string; ok: boolean }) => void;
	} = $props();

	let info = $state<NodeInfo | null>(null);
	let busy = $state(false);
	let msg = $state('');
	let ok = $state(true);

	const running = $derived(vms.filter((v) => v.phase === 'Running'));

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

	async function evacuate() {
		busy = true;
		msg = '';
		let migrated = 0;
		let failed = 0;
		for (const vm of running) {
			try {
				await api.migrate(vm.namespace, vm.name);
				migrated++;
				onaction?.({ verb: 'Live-migration', namespace: vm.namespace, name: vm.name, ok: true });
			} catch (e) {
				if (e instanceof Unauthorized) return;
				failed++;
				onaction?.({ verb: 'Live-migration', namespace: vm.namespace, name: vm.name, ok: false });
			}
		}
		ok = failed === 0;
		msg = `Evacuation requested for ${migrated} VM${migrated === 1 ? '' : 's'}${failed ? `, ${failed} failed` : ''} — watch the migrations in the dock.`;
		busy = false;
	}
</script>

{#if info?.canCordon}
	<section class="max-w-2xl rounded border border-slate-200">
		<h3
			class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
		>
			Maintenance
		</h3>
		<div class="space-y-3 p-3">
			<div class="flex items-center gap-2 text-sm">
				<span class="text-slate-500">Scheduling:</span>
				{#if info.unschedulable}
					<span class="inline-flex items-center gap-1.5 font-medium text-amber-700">
						<Ban size={14} /> Cordoned
					</span>
				{:else}
					<span class="inline-flex items-center gap-1.5 font-medium text-green-700">
						<CheckCircle2 size={14} /> Schedulable
					</span>
				{/if}
			</div>
			<div class="flex flex-wrap items-center gap-2">
				<button
					onclick={toggleCordon}
					disabled={busy}
					class="flex items-center gap-1.5 rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
				>
					{#if info.unschedulable}<CheckCircle2 size={13} /> Uncordon{:else}<Ban size={13} /> Cordon{/if}
				</button>
				<button
					onclick={evacuate}
					disabled={busy || running.length === 0}
					title={running.length === 0 ? 'No running VMs to migrate' : 'Live-migrate every running VM off this node'}
					class="flex items-center gap-1.5 rounded border border-slate-300 px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
				>
					<MoveRight size={13} /> Evacuate ({running.length})
				</button>
			</div>
			<p class="text-xs text-slate-400">
				Cordon stops new VM placements here; running VMs stay until you evacuate. Live-migration
				needs another schedulable node with capacity.
			</p>
			{#if msg}
				<p class="text-xs {ok ? 'text-slate-600' : 'text-red-700'}">{msg}</p>
			{/if}
		</div>
	</section>
{/if}
