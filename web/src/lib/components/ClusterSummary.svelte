<script lang="ts">
	import { api, Unauthorized, type ClusterSummary } from '$lib/api';
	import { cores, bytes } from '$lib/format';
	import Ring from './Ring.svelte';

	let { onselect }: { onselect?: (namespace: string, name: string) => void } = $props();

	let data = $state<ClusterSummary | null>(null);

	async function load() {
		try {
			data = await api.clusterSummary();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			data = null;
		}
	}
	$effect(() => {
		load();
		const id = setInterval(load, 30000);
		return () => clearInterval(id);
	});

	// KubeVirt's phase label is lowercase ("running"); order known phases, capitalize
	// for display, and tolerate any others.
	const PHASE_ORDER = ['running', 'paused', 'stopped', 'pending', 'scheduling', 'succeeded', 'failed'];
	const phaseColor: Record<string, string> = {
		running: 'text-green-600',
		paused: 'text-amber-600',
		failed: 'text-red-600'
	};
	const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
</script>

{#if data}
	<div class="border-b border-slate-200 bg-slate-50 px-4 py-3">
		<div class="flex flex-wrap items-start gap-x-8 gap-y-3">
			<div class="flex gap-6">
				<Ring
					label="CPU"
					used={data.cpu.used}
					total={data.cpu.total}
					allocated={data.cpu.allocated ?? 0}
					unit="cores"
					color="#2563eb"
					spark={data.cpu.spark ?? []}
				/>
				<Ring
					label="Memory"
					used={data.memory.used}
					total={data.memory.total}
					allocated={data.memory.allocated ?? 0}
					unit="bytes"
					color="#0d9488"
					spark={data.memory.spark ?? []}
				/>
				<Ring
					label="Storage (guest)"
					used={data.storage.used}
					total={data.storage.total}
					unit="bytes"
					color="#7c3aed"
					spark={data.storage.spark ?? []}
				/>
			</div>

			<div>
				<div class="text-xs font-semibold tracking-wide text-slate-500 uppercase">
					Virtual machines
				</div>
				<div class="mt-2 flex gap-4">
					{#each Object.entries(data.vms)
						.filter(([, n]) => n > 0)
						.sort(([a], [b]) => PHASE_ORDER.indexOf(a) - PHASE_ORDER.indexOf(b)) as [phase, n] (phase)}
						<div class="text-center">
							<div class="text-xl font-semibold {phaseColor[phase] ?? 'text-slate-700'}">{n}</div>
							<div class="text-[11px] text-slate-500">{cap(phase)}</div>
						</div>
					{/each}
				</div>
			</div>

			<div class="min-w-[12rem] flex-1">
				<div class="text-xs font-semibold tracking-wide text-slate-500 uppercase">Top consumers</div>
				<ul class="mt-1.5 space-y-0.5 text-xs">
					{#each data.topCpu.slice(0, 3) as c (c.namespace + '/' + c.name)}
						<li class="flex items-baseline justify-between gap-2">
							<button
								onclick={() => onselect?.(c.namespace, c.name)}
								class="min-w-0 truncate text-slate-700 hover:text-blue-700 hover:underline">{c.name}</button
							>
							<span class="shrink-0 text-slate-400">{cores(c.value)} cores · {bytes(data.topMemory.find((m) => m.name === c.name && m.namespace === c.namespace)?.value ?? 0)}</span>
						</li>
					{/each}
				</ul>
			</div>
		</div>
	</div>
{/if}
