<script lang="ts">
	import { page } from '$app/state';
	import PoolPage from '#lib/workspace/desktops/PoolPage.svelte';
</script>

<PoolPage
	scope={{
		kind: 'pool',
		project: page.params.project!,
		namespace: page.params.namespace!,
		pool: page.params.pool!,
	}}
/>
