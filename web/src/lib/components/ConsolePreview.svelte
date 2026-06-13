<script lang="ts">
	import { Monitor } from 'lucide-svelte';
	import { screenshotURL } from '$lib/actions';
	import type { VM } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';

	// vCenter's Summary console thumbnail: a periodically-refreshed screenshot of
	// the VM's graphical console (KubeVirt's vnc/screenshot subresource), clicking
	// through to the live Console tab. Only for running VMs; if the screenshot
	// 404s (no graphics device / unsupported), it hides itself.
	let { vm, onopen }: { vm: VM; onopen: () => void } = $props();

	const running = $derived(vm.phase === 'Running');

	// Cache-busting tick: an img can't carry auth headers, so the cookie-auth'd
	// GET is refreshed by changing the query param. Reset per VM.
	let tick = $state(0);
	let failed = $state(false);
	const vmKey = $derived(`${vm.namespace}/${vm.name}`);
	$effect(() => {
		vmKey; // reset state on selection change
		failed = false;
		tick = Date.now();
	});
	// Refresh while running + visible (paused when backgrounded).
	$effect(() => {
		if (!running) return;
		return pollWhileVisible(() => (tick = Date.now()), 20000);
	});
</script>

{#if running && !failed}
	<button
		onclick={onopen}
		title="Open the live console"
		class="group relative block w-full overflow-hidden rounded border border-slate-200 bg-slate-900 xl:w-80 xl:shrink-0"
	>
		<img
			src={screenshotURL(vm, tick)}
			alt="Console preview"
			class="max-h-64 w-full object-contain"
			onerror={() => (failed = true)}
		/>
		<span
			class="absolute right-2 bottom-2 flex items-center gap-1.5 rounded bg-black/60 px-2 py-1 text-xs text-white opacity-0 transition group-hover:opacity-100"
		>
			<Monitor size={13} /> Open console
		</span>
	</button>
{/if}
