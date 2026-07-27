<script lang="ts">
	import { untrack } from 'svelte';
	import { api, Unauthorized, type Options, type VM } from '$lib/api';
	import { friendlyError } from '$lib/format';
	import { TBODY, TH, TH_LAST, THEAD, THEAD_TR } from '$lib/table';
	import ErrorNote from './ErrorNote.svelte';
	import StageModal from './StageModal.svelte';
	import StorageClassSelect from './StorageClassSelect.svelte';

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
	let loadError = $state(''); // the class-list fetch, distinct from the submit

	async function load() {
		try {
			options = await api.options();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			loadError = friendlyError(e);
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

	function stage() {
		return api.stageEdit(vm.namespace, vm.name, {
			sourceFile: vm.sourceFile,
			migrateVolumes: moves,
		});
	}
</script>

<StageModal
	title="Migrate storage — {vm.name}"
	size="lg"
	label={moves.length ? `Stage migration (${moves.length})` : 'Stage migration'}
	missing={moves.length ? [] : ['Pick a target class for at least one disk']}
	summary={moves.length
		? `Stages ${moves.map((m) => `${m.name} → ${m.storageClass}`).join(', ')}`
		: ''}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	<p class="text-xs text-ink-muted">
		Stages a live storage migration into <strong>Changes</strong>. When the pull request merges,
		KubeVirt copies each disk to a new volume on the target class while the VM keeps running — the
		VM must still be running then, and the cluster must support volume migration. Reverting the
		merged change cancels an in-flight migration.
	</p>

	<table class="w-full text-[13px]">
		<thead class={THEAD}>
			<tr class={THEAD_TR}>
				<th class={TH}>Disk</th>
				<th class={TH}>Size</th>
				<th class={TH}>Current class</th>
				<th class={TH_LAST}>Target class</th>
			</tr>
		</thead>
		<tbody class={TBODY}>
			{#each disks as d (d.name)}
				<tr>
					<td class="py-1.5 pr-3 font-medium text-ink">{d.name}</td>
					<td class="py-1.5 pr-3 whitespace-nowrap text-ink-muted">{d.size || '—'}</td>
					<td class="py-1.5 pr-3 whitespace-nowrap text-ink-muted">
						{d.storageClass || 'cluster default'}
					</td>
					<td class="py-1.5">
						<StorageClassSelect
							options={options?.storageClasses ?? []}
							value={targets[d.name] ?? ''}
							onchange={(e) => (targets = { ...targets, [d.name]: e.currentTarget.value })}
							emptyLabel="— keep —"
							exclude={d.storageClass}
						/>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>

	<ErrorNote error={loadError} />
</StageModal>
