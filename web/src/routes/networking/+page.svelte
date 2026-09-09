<script lang="ts">
	import ContainerWorkspace from '$lib/workspace/ContainerWorkspace.svelte';
</script>

<ContainerWorkspace
	scope={{ kind: 'all' }}
	section="networking"
	trail={[{ label: 'All Networks' }]}
/>
