<script lang="ts">
	import { BookCopy } from '@lucide/svelte';
	import { api, type VM } from '#lib/api.js';
	import { templateGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { validName, NAME_HINT } from '#lib/validate.js';
	import FormField from './FormField.svelte';
	import StageModal from './StageModal.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';

	// Clone to Template: derive a reusable VirtualMachineTemplate from this VM's
	// git manifest and stage it into a library - the VM's own project, or the
	// shared (platform) library for curated golden templates. Both land as
	// templates/<name>.yaml behind the PR gate.
	let {
		vm,
		onclose,
	}: {
		vm: VM;
		onclose: () => void;
	} = $props();

	// svelte-ignore state_referenced_locally
	let name = $state(vm.name + '-template');
	let description = $state('');
	const own = $derived(templateGate('', vm.namespace));
	const shared = $derived(templateGate('platform', vm.namespace));
	// svelte-ignore state_referenced_locally
	let library = $state(!own.ok && shared.ok ? 'platform' : ''); // '' = the VM's own project

	const project = $derived(inventory.projectOf(vm.namespace));
	const valid = $derived(validName(name));
	const gate = $derived(library === 'platform' ? shared : own);
	const missing = $derived([
		...(valid ? [] : ['A valid template name is required']),
		...(gate.ok ? [] : [gate.why]),
	]);
	const summary = $derived(
		valid
			? `Stages templates/${name.trim()}.yaml → ${library === 'platform' ? 'shared library' : `project library${project ? ` (${project})` : ''}`}`
			: '',
	);

	async function stage() {
		await api.saveTemplate({
			library,
			name: name.trim(),
			description: description.trim(),
			sourceNamespace: vm.namespace,
			sourceName: vm.name,
		});
	}
</script>

<StageModal
	title="Clone to Template — {vm.name}"
	label="Stage template"
	{missing}
	{summary}
	onsubmit={stage}
	{onclose}
>
	{#snippet icon()}<BookCopy size={16} class="text-ink-muted" />{/snippet}
	<FormField label="Template name" error={name && !valid ? NAME_HINT : ''}>
		<TextInput bind:value={name} mono data-autofocus />
	</FormField>
	<FormField label="Description">
		<TextInput bind:value={description} placeholder="What this template provisions" />
	</FormField>
	<FormField label="Library">
		<SelectInput bind:value={library}>
			<option value="" disabled={!own.ok}
				>{project ? `Project library (${project})` : 'Project library'}</option
			>
			<option value="platform" disabled={!shared.ok}>Shared library</option>
		</SelectInput>
	</FormField>
	<p class="text-xs text-ink-faint">
		Derived from the VM’s git manifest: the name becomes a generated parameter, disks are
		re-anchored so every deploy is collision-free. Lands as templates/{name || '<name>'}.yaml when
		the PR merges.
	</p>
</StageModal>
