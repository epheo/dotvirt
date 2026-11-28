<script lang="ts">
	import { page } from '$app/state';
	import DesktopPage from '#lib/workspace/desktops/DesktopPage.svelte';
</script>

<DesktopPage
	scope={{
		kind: 'desktop',
		project: page.params.project!,
		namespace: page.params.namespace!,
		desktop: page.params.desktop!,
	}}
/>
