<script lang="ts">
	import type RFB from '@novnc/novnc';
	import { api, Unauthorized, type VM } from '#lib/api.js';
	import StatusDot from './StatusDot.svelte';

	let { vm }: { vm: VM } = $props();

	let screen = $state<HTMLDivElement>();
	let status = $state<'connecting' | 'connected' | 'disconnected'>('connecting');
	let detail = $state('');
	// The server ended the dotvirt session (logout, its lifetime cap, a
	// rejected token): a reconnect would be refused, so sign-in is offered.
	let ended = $state(false);
	let reconnectKey = $state(0); // bumping it re-runs the connect effect

	// The policy-violation close the server sends when the session behind a
	// console ends; noVNC's disconnect event does not carry the code.
	const SESSION_ENDED = 1008;

	// The viewer is handed an unopened WebSocket rather than a URL: owning the
	// socket is what exposes its close code.

	function wsURL(v: VM) {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		return `${proto}://${location.host}/api/vms/${encodeURIComponent(v.namespace)}/${encodeURIComponent(v.name)}/vnc`;
	}

	// The live stream hands down a fresh vm object every frame; keying the session
	// on it would drop and redial the connection each frame, so the connect effect
	// depends only on these stable primitives.
	const running = $derived(vm.phase === 'Running');
	const url = $derived(wsURL(vm));

	$effect(() => {
		// Re-create the RFB session when the target or running state changes, or on
		// an explicit reconnect.
		reconnectKey;
		if (!screen || !running) return;
		const target = screen;
		const socketURL = url;

		status = 'connecting';
		detail = '';
		ended = false;
		let rfb: RFB | null = null;
		let gone = false;
		// Loaded on first connect, so the VM page does not carry the viewer.
		import('@novnc/novnc').then(
			({ default: Viewer }) => {
				if (gone) return;
				const socket = new WebSocket(socketURL);
				// Registered before noVNC attaches its own handler, so it runs first.
				socket.addEventListener('close', (e) => (ended = e.code === SESSION_ENDED));
				rfb = new Viewer(target, socket);
				rfb.scaleViewport = true;
				rfb.background = '#151515';

				rfb.addEventListener('connect', () => (status = 'connected'));
				rfb.addEventListener('disconnect', (e: Event) => {
					status = 'disconnected';
					if (ended) detail = 'Your dotvirt session ended';
					else detail = (e as CustomEvent).detail?.clean ? 'Session ended' : 'Connection lost';
				});
				rfb.addEventListener('securityfailure', (e: Event) => {
					status = 'disconnected';
					detail = (e as CustomEvent).detail?.reason ?? 'Security failure';
				});
			},
			() => {
				if (gone) return;
				status = 'disconnected';
				detail = 'The console viewer did not load';
			},
		);

		return () => {
			gone = true;
			rfb?.disconnect();
		};
	});

	// The probe's 401 signs out centrally, which lands on the sign-in screen;
	// a session that answers is live after all, so the console redials.
	function signIn() {
		api.me().then(
			() => reconnectKey++,
			(e) => {
				if (!(e instanceof Unauthorized)) reconnectKey++;
			},
		);
	}
</script>

{#if !running}
	<div class="flex h-full items-center justify-center text-sm text-ink-faint">
		Console is available only while the VM is running (status: {vm.phase || 'stopped'}).
	</div>
{:else}
	<div class="flex h-full flex-col">
		<div class="flex items-center gap-2 px-1 pb-2 text-xs">
			<StatusDot
				tone={status === 'connected' ? 'ok' : status === 'connecting' ? 'warn' : 'danger'}
				pulse={status === 'connecting'}
			/>
			<span class="text-ink-muted capitalize">{status}{detail ? ` — ${detail}` : ''}</span>
		</div>
		<!-- The bezel behind the framebuffer stays dark in both themes (raw slate). -->
		<div class="relative min-h-0 flex-1 overflow-hidden rounded bg-[#151515]">
			<div bind:this={screen} class="h-full w-full"></div>
			{#if status === 'disconnected'}
				<div
					class="absolute inset-0 flex flex-col items-center justify-center gap-1 text-sm text-side-dim"
				>
					<span>{detail || 'Disconnected'}</span>
					{#if ended}
						<button
							onclick={signIn}
							class="rounded border border-side-active px-2 py-0.5 text-xs text-side-dim hover:bg-[#333638]"
							>Sign in again</button
						>
					{:else}
						<button
							onclick={() => reconnectKey++}
							class="rounded border border-side-active px-2 py-0.5 text-xs text-side-dim hover:bg-[#333638]"
							>Reconnect</button
						>
					{/if}
				</div>
			{/if}
		</div>
	</div>
{/if}
