<script lang="ts">
	import { Camera, RotateCcw, Trash2 } from 'lucide-svelte';
	import { api, Unauthorized, type Snapshot, type VM } from '$lib/api';

	let { vm }: { vm: VM } = $props();

	let snapshots = $state<Snapshot[] | null>(null);
	let loading = $state(false);
	let error = $state('');
	let taking = $state(false);
	let snapName = $state('');
	let busy = $state<string | null>(null); // snapshot being acted on
	let armedRestore = $state<string | null>(null);
	let armedDelete = $state<string | null>(null);

	// Restore needs a stopped target — KubeVirt rejects a running one.
	const running = $derived(vm.phase === 'Running');

	async function load() {
		loading = true;
		try {
			snapshots = await api.snapshots(vm.namespace, vm.name);
			error = '';
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		vm.namespace;
		vm.name;
		load();
	});

	// Poll while a snapshot is still being created so its status settles.
	$effect(() => {
		const pending = snapshots?.some((s) => !s.readyToUse && s.phase !== 'Failed');
		if (!pending) return;
		const id = setInterval(load, 4000);
		return () => clearInterval(id);
	});

	async function take() {
		taking = true;
		error = '';
		try {
			await api.takeSnapshot(vm.namespace, vm.name, snapName.trim() || undefined);
			snapName = '';
			await load();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			taking = false;
		}
	}

	async function restore(name: string) {
		armedRestore = null;
		busy = name;
		error = '';
		try {
			await api.restoreSnapshot(vm.namespace, vm.name, name);
			await load();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			busy = null;
		}
	}

	async function remove(name: string) {
		armedDelete = null;
		busy = name;
		error = '';
		try {
			await api.deleteSnapshot(vm.namespace, vm.name, name);
			await load();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			busy = null;
		}
	}

	function age(iso?: string): string {
		if (!iso) return '';
		const s = Math.max(0, Math.floor(Date.now() / 1000 - new Date(iso).getTime() / 1000));
		const d = Math.floor(s / 86400);
		const h = Math.floor((s % 86400) / 3600);
		const m = Math.floor((s % 3600) / 60);
		if (d > 0) return `${d}d ${h}h ago`;
		if (h > 0) return `${h}h ${m}m ago`;
		if (m > 0) return `${m}m ago`;
		return `${s}s ago`;
	}
</script>

<div class="space-y-4 p-1">
	<!-- Take a snapshot -->
	<div class="flex items-center gap-2">
		<input
			bind:value={snapName}
			placeholder="snapshot name (auto-generated if blank)"
			class="w-72 rounded border border-slate-300 px-2 py-1.5 text-sm"
		/>
		<button
			onclick={take}
			disabled={taking}
			class="flex items-center gap-1.5 rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-500 disabled:bg-slate-300"
		>
			<Camera size={14} /> {taking ? 'Taking…' : 'Take snapshot'}
		</button>
		{#if running}
			<span class="text-xs text-slate-400">Online snapshot (VM is running)</span>
		{/if}
	</div>

	{#if error}
		<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
	{/if}

	{#if snapshots && snapshots.length}
		<table class="w-full text-[13px]">
			<thead class="text-left text-xs tracking-wide text-slate-400 uppercase">
				<tr class="border-b border-slate-200">
					<th class="py-1.5 pr-3 font-medium">Name</th>
					<th class="py-1.5 pr-3 font-medium">Created</th>
					<th class="py-1.5 pr-3 font-medium">Status</th>
					<th class="py-1.5 font-medium"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100">
				{#each snapshots as s (s.name)}
					<tr>
						<td class="py-2 pr-3 font-medium text-slate-800">
							{s.name}
							{#if s.indications?.includes('Online')}
								<span class="ml-1 rounded bg-slate-100 px-1 text-[10px] text-slate-500">online</span>
							{/if}
						</td>
						<td class="py-2 pr-3 whitespace-nowrap text-slate-500">{age(s.created)}</td>
						<td class="py-2 pr-3 whitespace-nowrap">
							{#if s.readyToUse}
								<span class="inline-flex items-center gap-1.5 text-green-700">
									<span class="h-1.5 w-1.5 rounded-full bg-green-500"></span> Ready
								</span>
							{:else if s.phase === 'Failed'}
								<span class="inline-flex items-center gap-1.5 text-red-700" title={s.error}>
									<span class="h-1.5 w-1.5 rounded-full bg-red-500"></span> Failed
								</span>
							{:else}
								<span class="inline-flex items-center gap-1.5 text-amber-600">
									<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500"></span> Creating…
								</span>
							{/if}
						</td>
						<td class="py-2 text-right whitespace-nowrap">
							{#if busy === s.name}
								<span class="text-xs text-slate-400">working…</span>
							{:else}
								<button
									onclick={() => (armedRestore = armedRestore === s.name ? null : s.name)}
									disabled={!s.readyToUse || running}
									title={running ? 'Stop the VM to restore' : 'Roll the VM back to this snapshot'}
									class="mr-2 inline-flex items-center gap-1 text-xs text-amber-700 hover:underline disabled:text-slate-300 disabled:no-underline"
								>
									<RotateCcw size={12} /> {armedRestore === s.name ? 'Confirm restore' : 'Restore'}
								</button>
								{#if armedRestore === s.name}
									<button
										onclick={() => restore(s.name)}
										class="mr-2 rounded bg-amber-600 px-1.5 py-0.5 text-[11px] font-medium text-white"
										>Yes, restore</button
									>
								{/if}
								<button
									onclick={() => (armedDelete = armedDelete === s.name ? null : s.name)}
									class="inline-flex items-center gap-1 text-xs text-red-600 hover:underline"
								>
									<Trash2 size={12} /> {armedDelete === s.name ? 'Confirm delete' : 'Delete'}
								</button>
								{#if armedDelete === s.name}
									<button
										onclick={() => remove(s.name)}
										class="ml-2 rounded bg-red-600 px-1.5 py-0.5 text-[11px] font-medium text-white"
										>Yes, delete</button
									>
								{/if}
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{:else if loading && !snapshots}
		<p class="py-6 text-center text-sm text-slate-400">Loading snapshots…</p>
	{:else}
		<p class="py-6 text-center text-sm text-slate-400">
			No snapshots. Take one to capture the VM's current disk (and memory, if running) state —
			restore rolls it back later.
		</p>
	{/if}
</div>
