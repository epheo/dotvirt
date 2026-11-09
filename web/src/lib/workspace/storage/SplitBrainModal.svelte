<script lang="ts">
	import { untrack } from 'svelte';
	import { api, type ReplicatedVolume } from '#lib/api.js';
	import { relativeAge } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { replication } from '#lib/state/replication.svelte.js';
	import { size } from '#lib/storage.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import ChoiceCards from '#lib/components/ChoiceCards.svelte';
	import ConfirmDelete from '#lib/components/ConfirmDelete.svelte';
	import Consequence from '#lib/components/Consequence.svelte';

	// Which copy wins a split. The other copies' writes since the split are
	// gone for good, so the name is typed, and the copy in use is only a
	// suggestion: with no copy in use there is no default at all.
	let { volume, name, onclose }: { volume: ReplicatedVolume; name: string; onclose: () => void } =
		$props();

	const copies = $derived(volume.replicas.filter((r) => r.role === 'diskful'));
	let pick = $state(
		untrack(() =>
			volume.replicas.some((r) => r.role === 'diskful' && r.node === volume.primary)
				? (volume.primary ?? '')
				: '',
		),
	);
	const options = $derived(
		copies.map((r) => ({
			value: r.node,
			label: `Keep the copy on ${r.node}`,
			hint: r.node === volume.primary ? 'In use now. Recommended.' : 'Not in use now.',
		})),
	);
	const others = $derived(
		copies
			.map((r) => r.node)
			.filter((n) => n !== pick)
			.join(', '),
	);
	const op = action();

	async function keep() {
		const done = await op.run(() =>
			api.resolveSplitBrain(volume.name, pick, volume.splitSince ?? ''),
		);
		if (!done) return;
		ui.showToast(`Keeping the copy on ${pick}. The others rebuild from it.`);
		await replication.pull(true);
		onclose();
	}
</script>

<ConfirmDelete
	title="Two copies of {name} disagree"
	confirmWord={name}
	verb={pick ? `Keep ${pick}, discard the rest` : 'Pick a copy to keep'}
	busy={op.busy || !pick}
	error={op.error}
	onconfirm={keep}
	{onclose}
>
	<div class="flex flex-col gap-3">
		<p class="text-xs text-ink-muted">
			<span class="font-mono">{volume.name}</span>, {size(volume.sizeBytes)}, {volume.tier}{volume.splitSince
				? `, split ${relativeAge(volume.splitSince)}`
				: ''}
		</p>
		<p>The copies stopped seeing each other and both kept writing. They cannot be merged.</p>
		<ChoiceCards {options} bind:value={pick} />
		{#if pick}
			<div class="flex flex-col gap-1.5">
				<Consequence tone="danger">Writes made on {others} since the split are lost.</Consequence>
				<Consequence tone="info">{others} rebuilds from the copy on {pick}.</Consequence>
				<Consequence>Runs now, under your account. It is not a pull request.</Consequence>
			</div>
		{/if}
	</div>
</ConfirmDelete>
