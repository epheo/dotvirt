<script lang="ts">
	import { Monitor } from 'lucide-svelte';
	import { screenshotURL } from '$lib/actions';
	import type { VM } from '$lib/api';
	import { pollWhileVisible } from '$lib/poll';

	// vCenter's Summary console thumbnail: a periodically-refreshed screenshot of
	// the VM's graphical console (KubeVirt's vnc/screenshot subresource), clicking
	// through to the live Console tab. Only for running VMs; while the screenshot
	// fails (no graphics device, or a restart blip) it hides itself but keeps
	// probing, so a transient error never hides the preview for the session.
	let { vm, onopen }: { vm: VM; onopen: () => void } = $props();

	const running = $derived(vm.phase === 'Running');

	// Cache-busting tick: an img can't carry auth headers, so the cookie-auth'd
	// GET is refreshed by changing the query param. Reset per VM.
	let tick = $state(0);
	let failed = $state(false);
	// The bezel takes the framebuffer's own aspect so the preview is shaped
	// like the console really is — never a letterboxed stretch. 4:3 (the VGA
	// default) stands in until the first frame reports its true size.
	let aspect = $state('4 / 3');
	const vmKey = $derived(`${vm.namespace}/${vm.name}`);
	$effect(() => {
		vmKey; // reset state on selection change
		failed = false;
		aspect = '4 / 3';
		tick = Date.now();
	});
	// Refresh while running + visible (paused when backgrounded).
	$effect(() => {
		if (!running) return;
		return pollWhileVisible(() => (tick = Date.now()), 20000);
	});
</script>

{#if running}
	<!-- The bezel behind the framebuffer stays dark in both themes (raw slate). -->
	<!-- xl: the host row stretches the bezel to the tiles+usage column height
	     and aspect-ratio derives the width from it, so the preview spans the
	     column at the console's real proportions (max-w guards against very
	     wide framebuffers eating the row). -->
	<!-- Hidden (not unmounted) on failure: the polled img keeps probing, and
	     the next good frame brings the preview back. -->
	<button
		onclick={onopen}
		title="Open the live console"
		style:aspect-ratio={aspect}
		class="group relative block w-full max-w-xl overflow-hidden rounded border border-line bg-slate-900 xl:w-auto xl:shrink-0"
		class:hidden={failed}
	>
		<img
			src={screenshotURL(vm, tick)}
			alt="Console preview"
			class="h-full w-full object-contain"
			onload={(e) => {
				failed = false;
				const t = e.currentTarget as HTMLImageElement;
				if (t.naturalWidth && t.naturalHeight) aspect = `${t.naturalWidth} / ${t.naturalHeight}`;
			}}
			onerror={() => (failed = true)}
		/>
		<span
			class="absolute right-2 bottom-2 flex items-center gap-1.5 rounded bg-black/60 px-2 py-1 text-xs text-white opacity-0 transition group-hover:opacity-100"
		>
			<Monitor size={13} /> Open console
		</span>
	</button>
{/if}
