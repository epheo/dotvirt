<script lang="ts">
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import CatalogBrowser from '$lib/components/CatalogBrowser.svelte';
</script>

<Breadcrumb trail={[{ label: 'Catalog' }]} />
<CatalogBrowser />
