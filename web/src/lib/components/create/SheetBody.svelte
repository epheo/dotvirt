<script lang="ts">
	import type { Snippet } from 'svelte';
	import ErrorNote from '../ErrorNote.svelte';

	// A sheet type's two columns and footer: the form, what it stages, and the
	// verbs. Narrow screens stack the columns and scroll them as one.
	let {
		form,
		aside,
		footer,
		error = '',
	}: { form: Snippet; aside?: Snippet; footer: Snippet; error?: string } = $props();
</script>

<div
	class="grid min-h-0 flex-1 grid-cols-1 overflow-y-auto sm:grid-cols-[minmax(0,1fr)_296px] sm:overflow-hidden"
>
	<div class="flex min-w-0 flex-col gap-4 px-6 py-5 sm:overflow-y-auto">
		{@render form()}
		<ErrorNote {error} />
	</div>
	{#if aside}
		<aside
			aria-label="What this stages"
			class="flex min-w-0 flex-col gap-3 border-line-soft bg-inset/60 p-5 max-sm:border-t sm:overflow-y-auto sm:border-l"
		>
			{@render aside()}
		</aside>
	{/if}
</div>
<footer
	class="flex flex-wrap items-center gap-3 border-t border-line-soft bg-inset px-6 py-3.5 text-[13px]"
>
	{@render footer()}
</footer>
