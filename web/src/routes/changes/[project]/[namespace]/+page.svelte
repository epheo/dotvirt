<script lang="ts">
	import { page } from '$app/state';
	import ChangesWorkspace from '$lib/workspace/ChangesWorkspace.svelte';
</script>

<ChangesWorkspace project={page.params.project!} namespace={page.params.namespace!} />
