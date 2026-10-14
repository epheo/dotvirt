<script lang="ts">
	import FormField from '../FormField.svelte';
	import NamespaceSelect from '../NamespaceSelect.svelte';
	import TextInput from '../TextInput.svelte';

	let {
		name = $bindable(),
		namespace = $bindable(),
		namespaces,
		initialNamespace,
		nudged,
	}: {
		name: string;
		namespace: string;
		namespaces: string[];
		initialNamespace: string;
		nudged: boolean;
	} = $props();
</script>

<div class="space-y-4">
	<p class="text-xs text-ink-muted">
		Name the virtual machine and choose the project it belongs to.
	</p>
	<FormField label="Name" error={nudged && !name ? 'Name is required' : ''}>
		<TextInput bind:value={name} placeholder="my-vm" />
	</FormField>
	<NamespaceSelect bind:namespace {namespaces} initial={initialNamespace} fallback="default" />
</div>
