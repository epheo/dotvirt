<script lang="ts">
	import { Camera, RotateCcw, Trash2 } from 'lucide-svelte';
	import { api, type Snapshot, type VM } from '$lib/api';
	import { relativeAge } from '$lib/format';
	import { action, resource, type Resource } from '$lib/resource.svelte';
	import { TBODY, TH, TH_LAST, THEAD, THEAD_TR } from '$lib/table';
	import ErrorNote from './ErrorNote.svelte';
	import Note from './Note.svelte';
	import StatusDot from './StatusDot.svelte';
	import TextInput from './TextInput.svelte';

	let { vm }: { vm: VM } = $props();

	// Two actions because two busy displays run independently: the Take button's
	// label and the per-row "working…" (identified by busyName).
	const takeOp = action();
	const rowOp = action();
	let snapName = $state('');
	let busyName = $state<string | null>(null); // snapshot being acted on
	let armedRestore = $state<string | null>(null);
	let armedDelete = $state<string | null>(null);

	// Restore needs a stopped target — KubeVirt rejects a running one.
	const running = $derived(vm.phase === 'Running');

	// Keyed on the VM identity (the live stream hands down a fresh vm each
	// frame); polls only while a snapshot is still settling.
	const vmKey = $derived(`${vm.namespace}/${vm.name}`);
	// The explicit binding type breaks the inference cycle through the poll
	// gate (snapRes -> poll -> pending -> snapshots -> snapRes).
	const snapRes: Resource<Snapshot[]> = resource(
		() => vmKey,
		() => api.snapshots(vm.namespace, vm.name),
		{ reset: true, poll: () => (pending ? 4000 : 0) },
	);
	const snapshots = $derived(snapRes.data);
	const loading = $derived(snapRes.loading);
	const error = $derived(takeOp.error || rowOp.error || snapRes.error);
	const pending = $derived(snapshots?.some((s) => !s.readyToUse && s.phase !== 'Failed') ?? false);

	async function take() {
		rowOp.clear(); // one error slot: a new op supersedes the other's failure
		await takeOp.run(async () => {
			await api.takeSnapshot(vm.namespace, vm.name, snapName.trim() || undefined);
			snapName = '';
			await snapRes.refresh();
		});
	}

	async function rowAction(name: string, fn: () => Promise<unknown>) {
		takeOp.clear();
		busyName = name;
		await rowOp.run(async () => {
			await fn();
			await snapRes.refresh();
		});
		busyName = null;
	}

	function restore(name: string) {
		armedRestore = null;
		return rowAction(name, () => api.restoreSnapshot(vm.namespace, vm.name, name));
	}

	function remove(name: string) {
		armedDelete = null;
		return rowAction(name, () => api.deleteSnapshot(vm.namespace, vm.name, name));
	}
</script>

<div class="space-y-4 p-1">
	<!-- Take a snapshot -->
	<div class="flex items-center gap-2">
		<TextInput
			bind:value={snapName}
			placeholder="snapshot name (auto-generated if blank)"
			class="w-72!"
		/>
		<button
			onclick={take}
			disabled={takeOp.busy}
			class="flex items-center gap-1.5 rounded bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:bg-line-strong"
		>
			<Camera size={14} />
			{takeOp.busy ? 'Taking…' : 'Take snapshot'}
		</button>
		{#if running}
			<span class="text-xs text-ink-faint">Online snapshot (VM is running)</span>
		{/if}
	</div>

	<ErrorNote {error} />

	<!-- Restore needs a stopped VM (KubeVirt rejects a running target), but power
	     is PR-gated — so spell out the path rather than just greying the button. -->
	{#if running && snapshots?.some((s) => s.readyToUse)}
		<Note tone="warn" border>
			Restore is disabled while the VM is running. Set its power to <strong>Off</strong> (via a pull request
			from Edit Settings), and once it's stopped you can roll back to a snapshot here.
		</Note>
	{/if}

	{#if snapshots && snapshots.length}
		<table class="w-full text-[13px]">
			<thead class={THEAD}>
				<tr class={THEAD_TR}>
					<th class={TH}>Name</th>
					<th class={TH}>Created</th>
					<th class={TH}>Status</th>
					<th class={TH_LAST}></th>
				</tr>
			</thead>
			<tbody class={TBODY}>
				{#each snapshots as s (s.name)}
					<tr>
						<td class="py-2 pr-3 font-medium text-ink">
							{s.name}
							{#if s.indications?.includes('Online')}
								<span class="ml-1 rounded bg-inset-strong px-1 text-[10px] text-ink-muted"
									>online</span
								>
							{/if}
						</td>
						<td class="py-2 pr-3 whitespace-nowrap text-ink-muted">{relativeAge(s.created)}</td>
						<td class="py-2 pr-3 whitespace-nowrap">
							{#if s.readyToUse}
								<span class="inline-flex items-center gap-1.5 text-ok-ink">
									<StatusDot tone="ok" size="xs" /> Ready
								</span>
							{:else if s.phase === 'Failed'}
								<span class="inline-flex items-center gap-1.5 text-danger-ink" title={s.error}>
									<StatusDot tone="danger" size="xs" /> Failed
								</span>
							{:else}
								<span class="inline-flex items-center gap-1.5 text-warn-ink">
									<StatusDot tone="warn" size="xs" pulse /> Creating…
								</span>
							{/if}
						</td>
						<td class="py-2 text-right whitespace-nowrap">
							{#if busyName === s.name}
								<span class="text-xs text-ink-faint">working…</span>
							{:else}
								<button
									onclick={() => (armedRestore = armedRestore === s.name ? null : s.name)}
									disabled={!s.readyToUse || running}
									title={running ? 'Stop the VM to restore' : 'Roll the VM back to this snapshot'}
									class="mr-2 inline-flex items-center gap-1 text-xs text-warn-ink hover:underline disabled:text-ink-faint disabled:no-underline"
								>
									<RotateCcw size={12} />
									{armedRestore === s.name ? 'Confirm restore' : 'Restore'}
								</button>
								{#if armedRestore === s.name}
									<button
										onclick={() => restore(s.name)}
										class="mr-2 rounded bg-warn px-1.5 py-0.5 text-[11px] font-medium text-white"
										>Yes, restore</button
									>
								{/if}
								<button
									onclick={() => (armedDelete = armedDelete === s.name ? null : s.name)}
									class="inline-flex items-center gap-1 text-xs text-danger hover:underline"
								>
									<Trash2 size={12} />
									{armedDelete === s.name ? 'Confirm delete' : 'Delete'}
								</button>
								{#if armedDelete === s.name}
									<button
										onclick={() => remove(s.name)}
										class="ml-2 rounded bg-danger px-1.5 py-0.5 text-[11px] font-medium text-white"
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
		<p class="py-6 text-center text-sm text-ink-faint">Loading snapshots…</p>
	{:else}
		<p class="py-6 text-center text-sm text-ink-faint">
			No snapshots. Take one to capture the VM's current disk (and memory, if running) state —
			restore rolls it back later.
		</p>
	{/if}
</div>
