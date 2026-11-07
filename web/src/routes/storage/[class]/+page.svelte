<script lang="ts">
	import { page } from '$app/state';
	import ClassPage from '#lib/workspace/storage/ClassPage.svelte';
</script>

<ClassPage name={page.params.class!} />
