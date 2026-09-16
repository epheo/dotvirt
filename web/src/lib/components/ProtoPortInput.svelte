<script lang="ts">
	import SelectInput from './SelectInput.svelte';
	import TextInput from './TextInput.svelte';

	// Protocol + optional port pair shared by the firewall rule rows.
	// port is number | null, not string: <input type="number"> coerces its
	// binding to a number (or null when cleared), so a string type would make
	// `.trim()` throw in the callers.
	// The egress modal's wrapper row already styles the "port" label, so it
	// passes labelClass="" to keep inheriting instead of the row default here.
	let {
		proto = $bindable('TCP'),
		port = $bindable(null),
		portWidth = 'w-20',
		labelClass = 'text-xs text-ink-faint',
	}: {
		proto?: 'TCP' | 'UDP' | 'SCTP';
		port?: number | null;
		portWidth?: string;
		labelClass?: string;
	} = $props();
</script>

<span class={labelClass}>port</span>
<SelectInput bind:value={proto} size="sm" width="w-auto">
	<option value="TCP">TCP</option>
	<option value="UDP">UDP</option>
	<option value="SCTP">SCTP</option>
</SelectInput>
<TextInput
	type="number"
	bind:value={port}
	size="sm"
	placeholder="any"
	min="1"
	max="65535"
	width={portWidth}
/>
