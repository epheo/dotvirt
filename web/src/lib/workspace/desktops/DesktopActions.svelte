<script lang="ts">
	import { ChevronDown } from '@lucide/svelte';
	import { api } from '#lib/api.js';
	import { person, type DesktopRow } from '#lib/desktops.js';
	import { desktopGate } from '#lib/gates.js';
	import { action } from '#lib/resource.svelte.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { session } from '#lib/state/session.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import ConfirmDelete from '#lib/components/ConfirmDelete.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import ContextMenu from '#lib/components/ContextMenu.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import MenuItem from '#lib/components/MenuItem.svelte';
	import Modal from '#lib/components/Modal.svelte';

	// The acts on one desktop, run now under the caller's account. A pool's
	// desktop starts, stops and is released on the claim holding it; one made
	// one by one only restarts here, its power being git's. The rights are
	// asked when the menu first opens, and what an act changes arrives on the
	// live stream once DeskuBe writes it.
	let { row }: { row: DesktopRow } = $props();

	const d = $derived(row.desktop);
	const claim = $derived(row.claim);
	const pool = $derived(d.pool ?? '');
	const who = $derived(person(row));
	const own = $derived(!!claim && claim.user === session.user?.username);

	let menu = $state<{ x: number; y: number } | null>(null);
	let confirming = $state<'release' | 'restart' | null>(null);
	const run = action({ toast: ui.toastError });
	const confirm = action();

	// What holds a claim act back before the caller's rights: no claim to act on.
	const unheld = $derived(
		claim
			? ''
			: desktops.may?.lists(d.namespace, 'vdiclaims')
				? 'No one holds this desktop: its pool keeps it warm.'
				: 'Claims here are not shown to you.',
	);
	const stopping = $derived(claim?.running !== false);
	const power = $derived(unheld || desktopGate.power(d.namespace, pool, own).why);
	const release = $derived(unheld || desktopGate.release(d.namespace, pool, own).why);
	const restart = $derived(
		d.running
			? desktopGate.restart(d.namespace, pool).why
			: 'This desktop is stopped: it has no session to end.',
	);

	// The menu's footprint: under its button, or over it near the bottom.
	const MENU_W = 192;
	const MENU_H = 110;
	function open(e: MouseEvent) {
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		const below = r.bottom + 4 + MENU_H <= window.innerHeight;
		menu = { x: r.right - MENU_W, y: below ? r.bottom + 4 : r.top - 4 - MENU_H };
	}

	function pick(fn: () => void) {
		menu = null;
		fn();
	}

	async function setPower() {
		const name = d.name;
		const verb = stopping ? 'Stop' : 'Start';
		const act = stopping ? 'stop' : 'start';
		const done = await run.run(() => api.desktopAct(act, d.namespace, name, claim?.name));
		if (done) ui.showToast(`${verb} requested for ${name}.`, { kind: 'success' });
	}

	// The dialog closes on success, so what the toast names is read first.
	async function confirmed() {
		const name = d.name;
		const target = { namespace: d.namespace, claim: claim?.name, who };
		const act = confirming;
		if (!act) return;
		const done = await confirm.run(() =>
			api.desktopAct(act, target.namespace, name, act === 'release' ? target.claim : ''),
		);
		if (!done) return;
		confirming = null;
		ui.showToast(
			act === 'release'
				? `Released ${name}: ${target.who}'s desktop and home are being deleted.`
				: `Restart requested for ${name}.`,
			{ kind: 'success' },
		);
	}

	function close() {
		confirming = null;
		confirm.clear();
	}
</script>

<button
	onclick={open}
	disabled={run.busy}
	aria-label="Actions for {d.name}"
	aria-expanded={!!menu}
	class="flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
>
	Actions <ChevronDown size={12} />
</button>

{#if menu}
	<ContextMenu x={menu.x} y={menu.y} height={MENU_H} onclose={() => (menu = null)}>
		<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
			{#if d.pool}
				<MenuItem disabled={!!power} title={power} onclick={() => pick(setPower)}>
					{stopping ? 'Stop' : 'Start'}
				</MenuItem>
			{/if}
			<MenuItem
				disabled={!!restart}
				title={restart}
				onclick={() => pick(() => (confirming = 'restart'))}
			>
				Restart...
			</MenuItem>
			{#if d.pool}
				<div class="my-1 border-t border-line-soft"></div>
				<MenuItem
					danger
					disabled={!!release}
					title={release}
					onclick={() => pick(() => (confirming = 'release'))}
				>
					Release...
				</MenuItem>
			{/if}
		</div>
	</ContextMenu>
{/if}

{#if confirming === 'release'}
	<ConfirmDelete
		title="Release {d.name}"
		confirmWord={d.name}
		verb="Release"
		busy={confirm.busy}
		error={confirm.error}
		onconfirm={confirmed}
		onclose={close}
	>
		<div class="flex flex-col gap-3">
			<p>
				Deletes {who}'s claim <span class="font-mono">{claim?.name}</span> on pool {pool}.
			</p>
			<div class="flex flex-col gap-1.5">
				<Consequence tone="danger">
					{d.name} and its home, {who}'s files, are deleted for good.
				</Consequence>
				<Consequence tone="warn">{who}'s session, if one is open, ends.</Consequence>
				<Consequence>Runs now, under your account. It is not a pull request.</Consequence>
			</div>
		</div>
	</ConfirmDelete>
{:else if confirming === 'restart'}
	<Modal title="Restart {d.name}?" onclose={close}>
		<div class="flex flex-col gap-1.5 px-5 py-4">
			<Consequence tone="warn">{who ? `${who}'s session` : 'Its session'} ends.</Consequence>
			<Consequence tone="info">A new pod starts. The home, and the files in it, stay.</Consequence>
			<Consequence>Runs now, under your account. It is not a pull request.</Consequence>
			<ErrorNote error={confirm.error} />
		</div>
		{#snippet footer()}
			<Button variant="secondary" class="ml-auto" onclick={close}>Cancel</Button>
			<Button onclick={confirmed} disabled={confirm.busy}>
				{confirm.busy ? 'Restarting...' : 'Restart'}
			</Button>
		{/snippet}
	</Modal>
{/if}
