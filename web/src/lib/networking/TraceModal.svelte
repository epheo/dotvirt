<script lang="ts">
	import type { VMRef } from '#lib/api.js';
	import Modal from '#lib/components/Modal.svelte';
	import TracePanel from '#lib/components/TracePanel.svelte';

	// Check connectivity, from anywhere: the trace with its source pinned when
	// the opener names a VM.
	let { source, onclose }: { source?: VMRef; onclose: () => void } = $props();
</script>

<Modal
	title={source ? `Check connectivity from ${source.name}` : 'Check connectivity'}
	subtitle="Simulates the rules. It sends no packet."
	size="3xl"
	{onclose}
>
	<div class="max-h-[70vh] overflow-y-auto p-4">
		<TracePanel source={source ? { namespace: source.namespace, vm: source.name } : undefined} />
	</div>
</Modal>
