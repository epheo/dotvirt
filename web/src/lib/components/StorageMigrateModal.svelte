<script lang="ts">
	import { untrack } from 'svelte';
	import { HardDrive } from 'lucide-svelte';
	import { api, Unauthorized, type Options, type VM } from '$lib/api';
	import Modal from './Modal.svelte';

	// Storage live migration (the Storage vMotion dialog): pick a target class
	// per disk; staging rewrites each disk's DataVolume template and sets
	// updateVolumesStrategy: Migration — all through the normal PR lane. On
	// merge KubeVirt copies each disk to a fresh volume on the target class
	// while the VM keeps running; reverting the commit cancels the migration.
	let {
		vm,
		onclose,
		onstaged,
	}: {
		vm: VM;
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// Only DataVolume-backed disks are migratable (the manifest owns their
	// provisioning); container/cloud-init/empty disks are listed nowhere here.
	const disks = $derived((vm.disks ?? []).filter((d) => d.type === 'dataVolume'));

	let options = $state<Options | null>(null);
	let targets = $state<Record<string, string>>({}); // disk name → target class ('' = keep)
	let busy = $state(false);
	let error = $state('');

	async function load() {
		try {
			options = await api.options();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		}
	}
	$effect(() => {
		untrack(load);
	});

	const moves = $derived(
		disks
			.map((d) => ({ name: d.name, storageClass: targets[d.name] ?? '' }))
			.filter((m) => m.storageClass && m.storageClass !== currentClass(m.name)),
	);

	function currentClass(disk: string): string {
		return disks.find((d) => d.name === disk)?.storageClass ?? '';
	}

	async function stage() {
		if (!moves.length) return;
		busy = true;
		error = '';
		try {
			await api.stageEdit(vm.namespace, vm.name, {
				sourceFile: vm.sourceFile,
				migrateVolumes: moves,
			});
			onstaged();
			onclose();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			busy = false;
		}
	}
</script>

<Modal title="Migrate storage — {vm.name}" size="lg" {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm text-slate-700">
		<p class="mb-3 text-xs text-slate-500">
			Stages a live storage migration into <strong>Changes</strong>. When the pull request merges,
			KubeVirt copies each disk to a new volume on the target class while the VM keeps running — the
			VM must still be running then, and the cluster must support volume migration. Reverting the
			merged change cancels an in-flight migration.
		</p>

		<table class="w-full text-[13px]">
			<thead class="text-left text-xs tracking-wide text-slate-400 uppercase">
				<tr class="border-b border-slate-200">
					<th class="py-1.5 pr-3 font-medium">Disk</th>
					<th class="py-1.5 pr-3 font-medium">Size</th>
					<th class="py-1.5 pr-3 font-medium">Current class</th>
					<th class="py-1.5 font-medium">Target class</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100">
				{#each disks as d (d.name)}
					<tr>
						<td class="py-1.5 pr-3 font-medium text-slate-800">{d.name}</td>
						<td class="py-1.5 pr-3 whitespace-nowrap text-slate-500">{d.size || '—'}</td>
						<td class="py-1.5 pr-3 whitespace-nowrap text-slate-500">
							{d.storageClass || 'cluster default'}
						</td>
						<td class="py-1.5">
							<select
								value={targets[d.name] ?? ''}
								onchange={(e) => (targets = { ...targets, [d.name]: e.currentTarget.value })}
								class="w-full rounded border border-slate-300 px-2 py-1"
							>
								<option value="">— keep —</option>
								{#each options?.storageClasses ?? [] as sc (sc.name)}
									{#if sc.name !== d.storageClass}
										<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
									{/if}
								{/each}
							</select>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>

		{#if error}
			<pre class="mt-3 rounded bg-red-50 p-2 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<button
			onclick={onclose}
			class="rounded border border-slate-300 px-3 py-1 text-sm text-slate-700 hover:bg-slate-50"
		>
			Cancel
		</button>
		<button
			onclick={stage}
			disabled={!moves.length || busy}
			class="ml-auto flex items-center gap-1.5 rounded bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-500 disabled:bg-slate-300"
		>
			<HardDrive size={14} />
			{busy ? 'Staging…' : moves.length ? `Stage migration (${moves.length})` : 'Stage migration'}
		</button>
	{/snippet}
</Modal>
