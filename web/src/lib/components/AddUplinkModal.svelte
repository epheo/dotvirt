<script lang="ts">
	import { api, type PhysicalAdapter, type UplinkCreate } from '$lib/api';
	import { validName, NAME_HINT } from '$lib/validate';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';

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

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!validName(name)) m.push('Name must be lowercase alphanumeric with dashes');
		if (!nic) m.push('Physical adapter is required');
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid ? `Stages uplink “${name}” on ${nic} (${node || 'all workers'}) → platform repo` : '',
	);

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
		<FormField label="Name (physical network)" error={name && !validName(name) ? NAME_HINT : ''}>
			<TextInput bind:value={name} placeholder="physnet-prod" mono data-autofocus />
		</FormField>
		<FormField label="Nodes">
			<SelectInput bind:value={node}>
				<option value="">All worker nodes</option>
				{#each nodes as n (n)}<option value={n}>{n}</option>{/each}
			</SelectInput>
		</FormField>
		<div class="grid grid-cols-2 gap-3">
			<FormField label="Physical adapter (NIC)">
				{#if nics.length}
					<SelectInput bind:value={nic}>
						{#each nics as n (n)}<option value={n}>{n}</option>{/each}
					</SelectInput>
				{:else}
					<TextInput bind:value={nic} placeholder="eno2" mono />
				{/if}
			</FormField>
			<FormField label="OVS bridge (optional)">
				<TextInput bind:value={bridge} placeholder={name ? `br-${name}` : 'br-physnet'} mono />
			</FormField>
		</div>
		<p class="rounded bg-warn-soft/60 px-3 py-2 text-xs text-warn-ink">
			Creates an OVS bridge enslaving this NIC on {node || 'all worker nodes'} and maps it to the physical
			network — it changes node networking, so review the PR carefully. Cluster-scoped — proposed to the
			platform repository. Requires the NMState operator.
		</p>
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage uplink"
			disabled={!valid}
			{missing}
			{summary}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
