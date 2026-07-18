<script lang="ts">
	import FormField from './FormField.svelte';
	import SelectInput from './SelectInput.svelte';

	// The staging modals' "Project (namespace)" row. The default is backfilled by
	// an effect, not an initializer: namespaces can arrive from the inventory
	// stream after mount, and an initializer would leave the select empty.
	let {
		namespace = $bindable(),
		namespaces,
		initial,
		fallback = '',
	}: {
		namespace: string;
		namespaces: string[];
		initial?: string;
		fallback?: string;
	} = $props();

	$effect(() => {
		if (!namespace) namespace = initial ?? namespaces[0] ?? fallback;
	});
</script>

<FormField label="Project (namespace)">
	<SelectInput bind:value={namespace}>
		{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
	</SelectInput>
</FormField>
