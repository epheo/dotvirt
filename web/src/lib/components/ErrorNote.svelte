<script lang="ts">
	// The one error readout: pre-wrap so multi-line backend messages keep their
	// shape. Margin/height context comes from the caller via class.
	let { error, class: cls = '' }: { error: string; class?: string } = $props();
</script>

{#if error}
	<pre
		class="rounded bg-danger-soft/60 p-3 text-xs whitespace-pre-wrap text-danger-ink {cls}">{error}</pre>
{/if}
