<script lang="ts">
	import { api, type NamespaceCreate } from '$lib/api';
	import { validName, NAME_HINT, validCIDR } from '$lib/validate';
	import StageModal from './StageModal.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';
	import VMNetworkFields from './VMNetworkFields.svelte';

	let {
		projects = [],
		project: initialProject,
		onclose,
		onstaged,
	}: {
		projects?: string[]; // repo-backed projects the namespace can join
		project?: string; // preselected project (e.g. from a project's context menu)
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let name = $state('');
	let project = $state('');
	let withNetwork = $state(true);
	let netName = $state('');
	let subnet = $state('');

	$effect(() => {
		if (!project) project = initialProject ?? projects[0] ?? '';
	});
	let netTouched = $state(false);

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!validName(name)) m.push('Name must be lowercase alphanumeric with dashes');
		if (!project) m.push('Project is required');
		if (withNetwork) {
			if (!netName) m.push('VM Network name is required');
			if (!subnet.trim()) m.push('Subnet is required for a primary network');
			else if (!validCIDR(subnet.trim())) m.push('Subnet must be a CIDR (e.g. 10.40.0.0/16)');
		}
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Stages namespace ${name} in ${project}${withNetwork ? ` + VM Network ${netName}` : ''}`
			: '',
	);

	async function stage() {
		const req: NamespaceCreate = { name, project };
		if (withNetwork) {
			// A VM Network is a primary UDN, which must do IPAM - the subnet is required.
			req.vmNetwork = { name: netName, subnet: subnet.trim() };
		}
		await api.createNamespace(req);
	}
</script>

<StageModal
	title="New Namespace"
	label="Stage namespace"
	{missing}
	{summary}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	<FormField label="Name" error={name && !validName(name) ? NAME_HINT : ''}>
		<TextInput bind:value={name} placeholder="tenant-c" mono data-autofocus />
	</FormField>
	<FormField label="Project">
		<SelectInput bind:value={project}>
			{#each projects as p (p)}<option value={p}>{p}</option>{/each}
		</SelectInput>
	</FormField>

	<label class="flex items-center gap-2">
		<input type="checkbox" bind:checked={withNetwork} />
		<span class="text-ink-soft">Add a VM Network — the namespace's primary Segment (Tier-1)</span>
	</label>

	{#if withNetwork}
		<VMNetworkFields base={name} bind:name={netName} bind:subnet bind:touched={netTouched} />
	{/if}

	<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
		Creates a namespace in this project (labeled so dotvirt adopts it){#if withNetwork}, with a
			primary "VM Network" its VMs attach to by default{/if}. Applied by the project's Argo app on
		merge.
	</p>
</StageModal>
