<script lang="ts">
	import type { PolicyRuleView } from '$lib/api';
	import { TONE_TEXT, type Tone } from '$lib/status';

	// A policy's rule summaries in the Security vocabulary: Direction / Action /
	// Peer / Ports, with the action toned like the Security view rows.
	let { rules }: { rules: PolicyRuleView[] } = $props();

	const actionTone = (action: string): Tone =>
		action === 'Deny' ? 'danger' : action === 'Allow' ? 'ok' : 'neutral';
</script>

<table class="w-full text-xs">
	<thead class="text-left tracking-wide text-ink-faint uppercase">
		<tr class="border-b border-line">
			<th class="py-1.5 pr-3 font-medium">Direction</th>
			<th class="py-1.5 pr-3 font-medium">Action</th>
			<th class="py-1.5 pr-3 font-medium">Peer</th>
			<th class="py-1.5 font-medium">Ports</th>
		</tr>
	</thead>
	<tbody class="divide-y divide-line-soft">
		{#each rules as r, i (i)}
			<tr>
				<td class="py-1.5 pr-3 text-ink-muted">{r.direction}</td>
				<td class="py-1.5 pr-3 font-medium {TONE_TEXT[actionTone(r.action)]}">{r.action}</td>
				<td class="py-1.5 pr-3 text-ink-soft">{r.peer || 'any'}</td>
				<td class="py-1.5 text-ink-soft">{r.ports || 'any'}</td>
			</tr>
		{/each}
	</tbody>
</table>
