<script lang="ts">
	import { api, type Network, type VM } from '#lib/api.js';
	import { buildEditRequest, seedEditForm, summarize, type EditSection } from '#lib/editform.js';
	import { quantityBytes } from '#lib/format.js';
	import { attachableNetworks } from '#lib/networks.js';
	import { nodeTargets } from '#lib/state/hosts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Wizard from './Wizard.svelte';
	import ComputeStep from './editsettings/ComputeStep.svelte';
	import SchedulingStep from './editsettings/SchedulingStep.svelte';
	import StorageStep from './editsettings/StorageStep.svelte';
	import NetworksStep from './editsettings/NetworksStep.svelte';
	import LabelsStep from './editsettings/LabelsStep.svelte';
	import ReviewStep from './editsettings/ReviewStep.svelte';

	let {
		vm,
		networks = [],
		onclose,
		initialSection,
	}: {
		vm: VM;
		networks?: Network[]; // port-group catalog (from the page), for the adapter picker
		onclose: () => void;
		// Opened from a Configure section: start the wizard on that step.
		initialSection?: EditSection;
	} = $props();

	// Re-pulls only after a boot-time failure, so opening the dialog is the retry.
	inventory.loadOptions();
	const options = $derived(inventory.options);

	// svelte-ignore state_referenced_locally
	let form = $state(seedEditForm(vm));

	// Start on the step the user opened from a Configure section (else Compute).
	// svelte-ignore state_referenced_locally
	let current = $state(
		{ compute: 0, scheduling: 1, storage: 2, network: 3, labels: 4 }[initialSection ?? 'compute'] ??
			0,
	);

	// Hosts for the pin picker; without node-list RBAC it degrades to a
	// free-text host list. Held here, not in the step, so revisiting Scheduling
	// never refetches.
	const hosts = nodeTargets();
	// svelte-ignore state_referenced_locally
	let pinText = $state(form.pin.join(' '));

	// Attachable secondaries for this VM's namespace = shared (CUDN) networks +
	// this namespace's own non-default networks (the primary "VM Network" backs the
	// default NIC, so it isn't an add-able adapter).
	const available = $derived(attachableNetworks(networks, vm.namespace));

	// The selected instancetype's own CPU/memory - for the read-only hint and to
	// seed the custom inputs when converting an instancetype VM to custom sizing.
	const selectedIT = $derived(
		(options?.instancetypes ?? []).find((i) => i.name === form.instancetype),
	);

	const dirty = $derived.by(() => {
		const r = buildEditRequest(vm, form);
		// Anything beyond the always-present sourceFile means a real change was made.
		return Object.keys(r).length > 1;
	});

	// The preference the staged VM will carry: the picked one, else the VM's own.
	const effPref = $derived(
		(options?.preferences ?? []).find((p) => p.name === (form.preference || vm.preference)),
	);
	// Preference minimums (spec.requirements) are enforced by KubeVirt's webhook
	// only at sync time - after the PR merged. Refuse them at stage time instead.
	const prefViolation = $derived.by(() => {
		if (form.mode !== 'custom' || !effPref) return '';
		if (effPref.minCPU && form.cpuCores && form.cpuCores < effPref.minCPU)
			return `The ${effPref.name} preference requires at least ${effPref.minCPU} vCPUs.`;
		if (effPref.minMemory && form.memory) {
			const min = quantityBytes(effPref.minMemory);
			const got = quantityBytes(form.memory);
			if (!isNaN(min) && !isNaN(got) && got < min)
				return `The ${effPref.name} preference requires at least ${effPref.minMemory} of memory.`;
		}
		return '';
	});

	// Compute step is flagged invalid when custom sizing is half-filled (the one
	// combination that would silently drop the sizing change) or below the
	// preference's minimums (the cluster would refuse the merged PR). Other states
	// are "optional" (no marker) since every edit field is optional.
	const computeValid = $derived(
		(form.mode === 'custom' && !(form.cpuCores && form.memory)) || prefViolation
			? false
			: undefined,
	);

	// What will be staged, derived from the same request the backend receives -
	// so the review never diverges from the commit.
	const summary = $derived(summarize(vm, form));

	const stage = () => api.stageEdit(vm.namespace, vm.name, buildEditRequest(vm, form));
</script>

{#snippet stepCompute()}
	<ComputeStep
		bind:form
		{vm}
		{options}
		optionsError={inventory.optionsError}
		{selectedIT}
		{prefViolation}
	/>
{/snippet}

{#snippet stepScheduling()}
	<SchedulingStep bind:form bind:pinText {vm} {hosts} />
{/snippet}

{#snippet stepStorage()}
	<StorageStep bind:form {options} />
{/snippet}

{#snippet stepNetworks()}
	<NetworksStep bind:form {vm} {available} />
{/snippet}

{#snippet stepLabels()}
	<LabelsStep bind:form />
{/snippet}

{#snippet review()}
	<ReviewStep {summary} />
{/snippet}

<Wizard
	title={`Edit Settings — ${vm.name}`}
	bind:current
	steps={[
		{ title: 'Compute', valid: computeValid, body: stepCompute },
		{ title: 'Scheduling', body: stepScheduling },
		{ title: 'Storage', body: stepStorage },
		{ title: 'Networks', body: stepNetworks },
		{ title: 'Labels', body: stepLabels },
		{ title: 'Ready to complete', body: review },
	]}
	canFinish={dirty && !prefViolation}
	finishLabel="Stage change"
	footerHint="Staged into Changes; review and open a PR there."
	onsubmit={stage}
	{onclose}
/>
