<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type ReplicationEvict } from '#lib/api.js';
	import { friendlyError, plural } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { replication } from '#lib/state/replication.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import ConfirmDelete from '#lib/components/ConfirmDelete.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import Modal from '#lib/components/Modal.svelte';

	// For a host that will not come back. The plan is the server's dry run of
	// this very move, so the dialog cannot promise what the move then skips.
	let {
		host,
		nameOf,
		onclose,
	}: { host: string; nameOf: (volume: string) => string; onclose: () => void } = $props();

	let plan = $state.raw<ReplicationEvict | null>(null);
	let failed = $state('');
	const op = action();

	onMount(async () => {
		try {
			plan = await api.evictHost(host, true);
		} catch (e) {
			failed = friendlyError(e);
		}
	});

	const count = $derived(plan?.moving.length ?? 0);
	const copies = $derived(plural(count, 'copy', 'copies'));

	async function move() {
		let out: ReplicationEvict | undefined;
		const done = await op.run(async () => {
			out = await api.evictHost(host);
		});
		if (!done || !out) return;
		const stayed = out.skipped?.length ?? 0;
		ui.showToast(
			`${plural(out.moving.length, 'copy', 'copies')} moving off ${host}.${stayed ? ` ${stayed} stayed.` : ''}`,
			{ kind: stayed ? 'warning' : 'info' },
		);
		await replication.pull(true);
		onclose();
	}
</script>

{#snippet stays()}
	{#if plan?.skipped?.length}
		<div class="flex flex-col gap-1.5">
			<h3 class="text-xs font-bold text-ink-muted">Stays on {host}</h3>
			<ul class="flex max-h-48 flex-col gap-1 overflow-y-auto text-xs">
				{#each plan.skipped as s (s.volume)}
					<li><strong class="text-ink">{nameOf(s.volume)}</strong>: {s.reason}</li>
				{/each}
			</ul>
		</div>
	{/if}
{/snippet}

{#if plan && count > 0}
	<ConfirmDelete
		title="Move copies off {host}"
		confirmWord={host}
		verb="Move {copies}"
		busy={op.busy}
		error={op.error}
		onconfirm={move}
		{onclose}
	>
		<div class="flex flex-col gap-3">
			<div class="flex flex-col gap-1.5">
				<Consequence tone="warn">
					{copies} rebuild on other hosts, read in full from their peers.
				</Consequence>
				<Consequence>
					A move waits while r-drbd cannot start it yet, for example during a snapshot.
				</Consequence>
				<Consequence>Runs now, under your account. It is not a pull request.</Consequence>
			</div>
			{@render stays()}
		</div>
	</ConfirmDelete>
{:else}
	<Modal title="Move copies off {host}" {onclose}>
		<div class="flex flex-col gap-3 px-5 py-4 text-sm text-ink-soft">
			<ErrorNote error={failed} />
			{#if !plan && !failed}
				<p class="text-ink-muted">Working out what can move...</p>
			{:else if plan}
				<p>Nothing can move off {host} right now.</p>
				{@render stays()}
			{/if}
		</div>
		{#snippet footer()}
			<Button variant="secondary" class="ml-auto" onclick={onclose}>Close</Button>
		{/snippet}
	</Modal>
{/if}
