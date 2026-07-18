<script lang="ts" generics="T extends string">
	// Card-style radio group (label + hint per option): the replacement for
	// native radios and for selects that cram meaning into option text.
	type Option = { value: T; label: string; hint?: string };

	let { options, value = $bindable() }: { options: Option[]; value?: T } = $props();
</script>

<div class="flex gap-2" role="radiogroup">
	{#each options as o (o.value)}
		<button
			type="button"
			role="radio"
			aria-checked={value === o.value}
			onclick={() => (value = o.value)}
			class="flex-1 rounded border px-3 py-2 text-left text-xs {value === o.value
				? 'border-accent bg-select-soft text-accent-ink'
				: 'border-line-strong text-ink-soft hover:bg-inset'}"
		>
			<div class="font-medium">{o.label}</div>
			{#if o.hint}<div class="text-ink-faint">{o.hint}</div>{/if}
		</button>
	{/each}
</div>
