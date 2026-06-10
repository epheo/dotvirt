<script lang="ts">
	import RFB from '@novnc/novnc';
	import type { VM } from '$lib/api';

	let { vm }: { vm: VM } = $props();

	let screen = $state<HTMLDivElement>();
	let status = $state<'connecting' | 'connected' | 'disconnected'>('connecting');
	let detail = $state('');

	function wsURL(v: VM) {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		return `${proto}://${location.host}/api/vms/${encodeURIComponent(v.namespace)}/${encodeURIComponent(v.name)}/vnc`;
	}

	$effect(() => {
		// Re-create the RFB session whenever the VM (or its running state) changes.
		if (!screen || vm.phase !== 'Running') return;

		status = 'connecting';
		detail = '';
		const rfb = new RFB(screen, wsURL(vm), {});
		rfb.scaleViewport = true;
		rfb.background = '#0f172a';

		rfb.addEventListener('connect', () => (status = 'connected'));
		rfb.addEventListener('disconnect', (e: Event) => {
			status = 'disconnected';
			detail = (e as CustomEvent).detail?.clean ? 'Session ended' : 'Connection lost';
		});
		rfb.addEventListener('securityfailure', (e: Event) => {
			status = 'disconnected';
			detail = (e as CustomEvent).detail?.reason ?? 'Security failure';
		});

		return () => rfb.disconnect();
	});
</script>

{#if vm.phase !== 'Running'}
	<div class="flex h-full items-center justify-center text-sm text-slate-400">
		Console is available only while the VM is running (status: {vm.phase || 'stopped'}).
	</div>
{:else}
	<div class="flex h-full flex-col">
		<div class="flex items-center gap-2 px-1 pb-2 text-xs">
			<span
				class="inline-block h-2 w-2 rounded-full {status === 'connected'
					? 'bg-green-500'
					: status === 'connecting'
						? 'animate-pulse bg-amber-400'
						: 'bg-red-500'}"
			></span>
			<span class="text-slate-500 capitalize">{status}{detail ? ` — ${detail}` : ''}</span>
		</div>
		<div bind:this={screen} class="min-h-0 flex-1 overflow-hidden rounded bg-slate-900"></div>
	</div>
{/if}
