<script lang="ts">
	import { page } from '$app/state';
	import NetworkPage from '#lib/networking/NetworkPage.svelte';

	// Rest param: a project network's key is "namespace/name".
	const key = $derived(page.params.network!);
</script>

{#key key}
	<NetworkPage {key} />
{/key}
