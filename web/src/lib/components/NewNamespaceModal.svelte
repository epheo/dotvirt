<script lang="ts">
	import { branding } from '#lib/state/branding.svelte.js';
	import { untrack } from 'svelte';
	import { api, type NamespaceCreate } from '#lib/api.js';
	import { clusterGate, projectGate, projectNS } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { validName, NAME_HINT, validCIDR } from '#lib/validate.js';
	import StageModal from './StageModal.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';
	import VMNetworkFields from './VMNetworkFields.svelte';

	let {
		projects = [],
		project: initialProject,
		onclose,
	}: {
		projects?: string[]; // repo-backed projects the namespace can join
		project?: string; // preselected project (e.g. from a project's context menu)
		onclose: () => void;
	} = $props();

	let name = $state('');
	let project = $state('');
	// The namespace's VM Network: its project's, which it joins, one of its
	// own, or none (the cluster's own network).
	let mode = $state<'project' | 'own' | 'none'>('own');
	const withNetwork = $derived(mode === 'own');
	let netName = $state('');
	let subnet = $state('');

	$effect(() => {
		if (!project) project = initialProject ?? projects[0] ?? '';
	});
	let netTouched = $state(false);

	const first = $derived(
		projectNS(inventory.projects.find((p) => p.name === project) ?? { namespaces: [] }),
	);
	const join = $derived(projectGate.newNamespace(first));
	// A project with a VM Network of its own: new namespaces join it unless told.
	const shared = $derived(!!caps.ns(first)?.project?.projectNetwork);
	$effect(() => {
		const s = shared;
		untrack(() => {
			if (s && mode === 'own') mode = 'project';
			if (!s && mode === 'project') mode = 'own';
		});
	});
	const net = $derived(clusterGate.vmNetwork());

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!validName(name)) m.push(NAME_HINT);
		if (!project) m.push('Project is required');
		else if (!join.ok) m.push(join.why);
		if (withNetwork && !net.ok) m.push(net.why);
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
			? `Stages namespace ${name} in ${project}${withNetwork ? ` + VM Network ${netName}` : mode === 'project' ? `, on ${project}'s VM Network` : ''}`
			: '',
	);

	async function stage() {
		const req: NamespaceCreate = { name, project };
		if (mode === 'project') req.projectNetwork = true;
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

	<FormField label="VM Network">
		<SelectInput bind:value={mode}>
			{#if shared}<option value="project">{project}'s VM Network</option>{/if}
			<option value="own">A VM Network of its own</option>
			<option value="none">None: the cluster's own network</option>
		</SelectInput>
	</FormField>

	{#if withNetwork}
		<VMNetworkFields base={name} bind:name={netName} bind:subnet bind:touched={netTouched} />
	{/if}

	<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
		Creates a namespace in this project (labeled so {branding.name} adopts it){#if withNetwork},
			with a primary "VM Network" its VMs attach to by default{:else if mode === 'project'}, on the
			project's VM Network, which its VMs attach to by default{/if}. A namespace takes a VM Network
		only when created. Applied by the project's Argo app on merge.
	</p>
</StageModal>
