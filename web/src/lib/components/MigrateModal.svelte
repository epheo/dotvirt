<script lang="ts">
	import { untrack } from 'svelte';
	import { MoveRight } from 'lucide-svelte';
	import { api, Unauthorized, type NodeTarget, type VM } from '$lib/api';
	import Modal from './Modal.svelte';

	// Live-migration target picker (the vMotion dialog): Automatic lets the
	// scheduler place the VMI; picking a host pins the migration to it via the
	// migration's added node selector — which can only narrow the VM's own
	// scheduling constraints, never bypass them. Listing hosts is cluster-scoped
	// RBAC; a caller without it keeps Automatic.
	let {
		vm,
		onclose,
		ondone,
	}: {
		vm: VM;
		onclose: () => void;
		// Reports the migrate request's outcome (for the Recent Tasks dock).
		ondone?: (ok: boolean) => void;
	} = $props();

	let nodes = $state<NodeTarget[] | null>(null);
	let canPick = $state(true);
	let target = $state(''); // '' = automatic
	let busy = $state(false);
	let error = $state('');

	async function load() {
		try {
			nodes = await api.nodes();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			canPick = false; // no node-list RBAC — the scheduler's choice only
			nodes = [];
		}
	}
	// Load once on mount (untracked: the host hands down a fresh vm each frame,
	// but this modal acts on the one it opened for).
	$effect(() => {
		untrack(load);
	});

	// Why a host can't be picked ('' = it can). The current host is excluded
	// because KubeVirt only migrates between distinct nodes.
	function blocked(n: NodeTarget): string {
		if (n.name === vm.nodeName) return 'current host';
		if (!n.ready) return 'not ready';
		if (n.unschedulable) return 'cordoned';
		return '';
	}

	async function migrate() {
		busy = true;
		error = '';
		try {
			await api.migrate(vm.namespace, vm.name, target || undefined);
			ondone?.(true);
			onclose();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
			ondone?.(false);
		} finally {
			busy = false;
		}
	}
</script>

<Modal title="Live-migrate — {vm.name}" size="lg" {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm text-ink-soft">
		<p class="mb-3 text-xs text-ink-muted">
			Moves the running VM to another host with no downtime. Currently on
			<span class="font-mono">{vm.nodeName || 'unknown host'}</span>.
		</p>

		<fieldset class="space-y-1">
			<label
				class="flex cursor-pointer items-center gap-2 rounded border px-3 py-2 {target === ''
					? 'border-blue-400 bg-blue-50/50'
					: 'border-slate-200 hover:bg-slate-50'}"
			>
				<input type="radio" bind:group={target} value="" />
				<span class="font-medium">Automatic</span>
				<span class="text-xs text-ink-muted">— the scheduler picks the best host</span>
			</label>

			{#if nodes === null && canPick}
				<p class="px-3 py-1 text-xs text-ink-faint">Loading hosts…</p>
			{:else if !canPick}
				<p class="px-3 py-1 text-xs text-ink-faint">
					Your account can't list hosts — placement stays with the scheduler.
				</p>
			{:else}
				{#each nodes ?? [] as n (n.name)}
					{@const why = blocked(n)}
					<label
						class="flex items-center gap-2 rounded border px-3 py-2 {why
							? 'cursor-not-allowed border-slate-100 text-ink-faint'
							: target === n.name
								? 'cursor-pointer border-blue-400 bg-blue-50/50'
								: 'cursor-pointer border-slate-200 hover:bg-slate-50'}"
					>
						<input type="radio" bind:group={target} value={n.name} disabled={!!why} />
						<span class="font-mono text-[13px]">{n.name}</span>
						{#if why}
							<span class="ml-auto text-xs">{why}</span>
						{/if}
					</label>
				{/each}
			{/if}
		</fieldset>

		{#if error}
			<pre class="mt-3 rounded bg-red-50 p-2 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<button
			onclick={onclose}
			class="rounded border border-slate-300 px-3 py-1 text-sm text-ink-soft hover:bg-slate-50"
		>
			Cancel
		</button>
		<button
			onclick={migrate}
			disabled={busy}
			class="ml-auto flex items-center gap-1.5 rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-500 disabled:bg-slate-300"
		>
			<MoveRight size={14} />
			{busy ? 'Migrating…' : 'Migrate'}
		</button>
	{/snippet}
</Modal>
