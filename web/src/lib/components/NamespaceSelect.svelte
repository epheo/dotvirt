<script lang="ts">
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

<label class="block">
	<span class="text-ink-soft">Project (namespace)</span>
	<select bind:value={namespace} class="mt-1 w-full rounded border border-line-strong px-2 py-1.5">
		{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
	</select>
</label>
