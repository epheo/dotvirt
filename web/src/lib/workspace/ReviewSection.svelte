<script lang="ts">
	import type { Snippet } from 'svelte';

	// One block of a review card: a bold 13px heading, then its body.
	let { title, children }: { title: string; children: Snippet } = $props();
</script>

<div class="flex flex-col gap-2 px-card py-3.5">
	<h3 class="text-[13px] leading-5 font-bold text-ink">{title}</h3>
	{@render children()}
</div>
