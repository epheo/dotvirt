<script lang="ts">
	import { validCIDR, CIDR_HINT } from '$lib/validate';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';

	// The primary VM Network sub-panel shared by the project and namespace
	// modals. The name defaults to "<base>-net" until the user overrides it;
	// touched lives in the parent so the override survives the panel's
	// checkbox toggling this component in and out.
	let {
		base = '',
		name = $bindable(''),
		subnet = $bindable(''),
		touched = $bindable(false),
	}: {
		base?: string;
		name?: string;
		subnet?: string;
		touched?: boolean;
	} = $props();

	$effect(() => {
		if (!touched) name = base ? `${base}-net` : '';
	});
</script>

<div class="space-y-3 rounded border border-line p-3">
	<FormField label="VM Network name">
		<TextInput bind:value={name} oninput={() => (touched = true)} mono />
	</FormField>
	<FormField
		label="Subnet (CIDR — required for a primary network)"
		error={subnet && !validCIDR(subnet.trim()) ? CIDR_HINT : ''}
	>
		<TextInput bind:value={subnet} placeholder="10.40.0.0/16" mono />
	</FormField>
	<p class="text-[11px] text-ink-faint">
		A flat layer-2 network that follows VMs across nodes (keeps their IP on migration).
	</p>
</div>
