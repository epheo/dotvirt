<script lang="ts">
	import { BookCopy } from 'lucide-svelte';
	import { api, Unauthorized, type VM } from '$lib/api';
	import { inventory } from '$lib/state/inventory.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import FormField from './FormField.svelte';
	import Modal from './Modal.svelte';

	// Clone to Template: derive a reusable VirtualMachineTemplate from this VM's
	// git manifest and stage it into a library — the VM's own project, or the
	// shared (platform) library for curated golden templates. Both land as
	// templates/<name>.yaml behind the PR gate.
	let {
		vm,
		onclose,
		onstaged,
	}: {
		vm: VM;
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// The prefill seeds from the VM the modal opened for (host closes it on
	// selection change, so the initial capture is the intent).
	// svelte-ignore state_referenced_locally
	let name = $state(vm.name + '-template');
	let description = $state('');
	let library = $state(''); // '' = the VM's own project
	let busy = $state(false);
	let error = $state('');

	const project = $derived(inventory.projectOf(vm.namespace));
	const valid = $derived(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name) && name.length <= 63);

	async function save() {
		busy = true;
		error = '';
		try {
			await api.saveTemplate({
				library,
				name: name.trim(),
				description: description.trim(),
				sourceNamespace: vm.namespace,
				sourceName: vm.name,
			});
			onstaged();
			onclose();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			busy = false;
		}
	}
</script>

<Modal title="Clone to Template — {vm.name}" {onclose}>
	{#snippet icon()}<BookCopy size={16} class="text-ink-muted" />{/snippet}
	<div class="space-y-3 px-5 py-4 text-sm">
		<FormField
			label="Template name"
			error={name && !valid ? 'Lowercase alphanumeric and “-”, max 63 characters.' : ''}
		>
			<input
				bind:value={name}
				class="w-full rounded border border-line px-2 py-1.5 font-mono text-sm"
			/>
		</FormField>
		<FormField label="Description">
			<input
				bind:value={description}
				placeholder="What this template provisions"
				class="w-full rounded border border-line px-2 py-1.5 text-sm"
			/>
		</FormField>
		<FormField label="Library">
			<select bind:value={library} class="w-full rounded border border-line px-2 py-1.5 text-sm">
				<option value="">{project ? `Project library (${project})` : 'Project library'}</option>
				<option value="platform">Shared library — needs template-curation permission</option>
			</select>
		</FormField>
		<p class="text-xs text-ink-faint">
			Derived from the VM’s git manifest: the name becomes a generated parameter, disks are
			re-anchored so every deploy is collision-free. Lands as templates/{name || '<name>'}.yaml when
			the PR merges.
		</p>
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<button
			onclick={onclose}
			class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong">Cancel</button
		>
		<button
			onclick={save}
			disabled={!valid || busy}
			class="rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-line-strong"
			>Stage template</button
		>
	{/snippet}
</Modal>
