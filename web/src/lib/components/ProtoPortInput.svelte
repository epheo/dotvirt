<script lang="ts">
	// Protocol + optional port pair shared by the firewall rule rows.
	// port is number | null, not string: <input type="number"> coerces its
	// binding to a number (or null when cleared), so a string type would make
	// `.trim()` throw in the callers.
	// The egress modal's wrapper row already styles the "port" label, so it
	// passes labelClass="" to keep inheriting instead of the row default here.
	let {
		proto = $bindable('TCP'),
		port = $bindable(null),
		portClass = 'w-20',
		labelClass = 'text-xs text-ink-faint',
	}: {
		proto?: 'TCP' | 'UDP' | 'SCTP';
		port?: number | null;
		portClass?: string;
		labelClass?: string;
	} = $props();
</script>

<span class={labelClass}>port</span>
<select bind:value={proto} class="rounded border border-line-strong px-1.5 py-1 text-xs">
	<option value="TCP">TCP</option>
	<option value="UDP">UDP</option>
	<option value="SCTP">SCTP</option>
</select>
<input
	type="number"
	bind:value={port}
	placeholder="any"
	min="1"
	max="65535"
	class="{portClass} rounded border border-line-strong px-2 py-1 text-xs"
/>
