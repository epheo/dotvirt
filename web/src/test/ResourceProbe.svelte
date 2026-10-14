<script lang="ts">
	import { resource } from '$lib/resource.svelte';

	// A minimal host for resource(): the component tests drive it through props
	// the way a detail panel's identity string changes under it.
	let {
		id,
		load,
		poll = 0,
	}: { id: string; load: (key: string) => Promise<string>; poll?: number } = $props();

	const r = resource(
		() => id,
		(k) => load(k),
		{ reset: true, poll: () => poll },
	);
</script>

{#if r.loading}
	<p>Loading…</p>
{:else if r.failed}
	<p role="alert">{r.error}</p>
{:else if r.data}
	<p data-testid="data">{r.data}</p>
{/if}
<button onclick={() => r.refresh()}>Refresh</button>
