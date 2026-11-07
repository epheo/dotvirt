<script lang="ts">
	import { Search } from '@lucide/svelte';

	// The rail tree's filter box, held at the top while the tree scrolls.
	let {
		value = $bindable(''),
		label,
		placeholder,
	}: { value?: string; label: string; placeholder: string } = $props();
</script>

<div class="sticky top-0 z-10 -mx-2.5 bg-side px-2.5 pt-2 pb-2">
	<div
		class="flex h-[30px] items-center gap-1.5 rounded-md border border-side-edge bg-bar px-2 text-side-dim focus-within:border-side-dim"
	>
		<Search size={14} class="shrink-0" />
		<input
			bind:value
			type="search"
			aria-label={label}
			{placeholder}
			onkeydown={(e) => {
				if (e.key === 'Escape') value = '';
			}}
			class="w-full min-w-0 bg-transparent text-[13px] text-side-ink placeholder-side-dim focus:outline-none"
		/>
	</div>
</div>
