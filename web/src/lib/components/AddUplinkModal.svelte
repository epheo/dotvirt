<script lang="ts">
	import { api, type PhysicalAdapter, type UplinkCreate } from '$lib/api';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

	let {
		adapters = [],
		onclose,
		onstaged,
	}: {
		adapters?: PhysicalAdapter[]; // node NICs (the port to enslave)
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let name = $state('');
	let nic = $state('');
	let bridge = $state('');
	let node = $state(''); // '' = all worker nodes; else pin to this hostname

	let submitting = $state(false);
	let error = $state('');

	// Nodes that report adapters, so the user can target one; '' = all workers.
	const nodes = $derived([...new Set(adapters.map((a) => a.node))].sort());
	// NICs on the chosen node (or across all nodes), available ones first.
	const nics = $derived([
		...new Set(
			[...adapters]
				.filter((a) => !node || a.node === node)
				.sort((a, b) => (a.role === 'available' ? -1 : 1))
				.map((a) => a.name),
		),
	]);

	$effect(() => {
		if (!nic || !nics.includes(nic)) nic = nics[0] ?? '';
	});

	const valid = $derived(!!(name && nic));

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		const req: UplinkCreate = { name, nic };
		if (bridge.trim()) req.bridge = bridge.trim();
		if (node) req.nodeSelector = { 'kubernetes.io/hostname': node };
		try {
			await api.createUplink(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title="Add Uplink" {onclose}>
	<div class="space-y-4 px-5 py-4 text-sm">
		<label class="block">
			<span class="text-slate-600">Name (physical network)</span>
			<input
				bind:value={name}
				placeholder="physnet-prod"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>
		<label class="block">
			<span class="text-slate-600">Nodes</span>
			<select bind:value={node} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
				<option value="">All worker nodes</option>
				{#each nodes as n (n)}<option value={n}>{n}</option>{/each}
			</select>
		</label>
		<div class="grid grid-cols-2 gap-3">
			<label class="block">
				<span class="text-slate-600">Physical adapter (NIC)</span>
				{#if nics.length}
					<select bind:value={nic} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
						{#each nics as n (n)}<option value={n}>{n}</option>{/each}
					</select>
				{:else}
					<input
						bind:value={nic}
						placeholder="eno2"
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					/>
				{/if}
			</label>
			<label class="block">
				<span class="text-slate-600">OVS bridge <span class="text-slate-400">(optional)</span></span
				>
				<input
					bind:value={bridge}
					placeholder={name ? `br-${name}` : 'br-physnet'}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
			</label>
		</div>
		<p class="rounded bg-amber-50 px-3 py-2 text-xs text-amber-700">
			Creates an OVS bridge enslaving this NIC on {node || 'all worker nodes'} and maps it to the physical
			network — it changes node networking, so review the PR carefully. Cluster-scoped — proposed to the
			platform repository. Requires the NMState operator.
		</p>
		{#if error}
			<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage uplink"
			disabled={!valid}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
