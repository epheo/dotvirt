<script lang="ts">
	import { num } from '#lib/storage.js';

	// Where a set of disks lives, as one bar: local (stays on its host) then
	// shared (its VM can live-migrate). Unknown disks are the empty track.
	let { local, shared, total }: { local: number; shared: number; total?: number } = $props();
	const all = $derived(Math.max(total ?? local + shared, 1));
</script>

<span
	role="img"
	aria-label="{num(local)} local, {num(shared)} shared"
	class="flex h-2 w-40 max-w-full overflow-hidden rounded bg-inset-strong"
>
	<span class="block h-2 bg-glyph" style:width="{(local / all) * 100}%"></span>
	<span class="block h-2 bg-glyph-2" style:width="{(shared / all) * 100}%"></span>
</span>
