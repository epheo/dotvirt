<script lang="ts">
	import { computeRoot } from '#lib/state/modules.svelte.js';
	import ContainerWorkspace from '#lib/workspace/ContainerWorkspace.svelte';
</script>

<ContainerWorkspace
	scope={{ kind: 'all' }}
	section="compute"
	trail={[{ label: computeRoot(), href: '/compute' }]}
/>
