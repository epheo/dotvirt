<script lang="ts">
	import { page } from '$app/state';
	import DesktopsScope from '#lib/workspace/desktops/DesktopsScope.svelte';
</script>

<DesktopsScope scope={{ kind: 'project', project: page.params.project! }} />
