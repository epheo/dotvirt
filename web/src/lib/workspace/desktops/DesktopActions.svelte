<script lang="ts">
	import { ChevronDown } from '@lucide/svelte';
	import { api, type Desktop, type DesktopClaim } from '#lib/api.js';
	import { person, restartHeld, type DesktopRow } from '#lib/desktops.js';
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

	// The acts on one desktop, run now under the caller's account: a restart of
	// any desktop, and on a pool's desktop a start, stop or release on the
	// claim holding it. The menu, and the dialog it opens, act on the desktop
	// and the claim shown when the menu opened, each named by its UID, and the
	// desktop by the person it was for: one deleted since, another made under
	// its name, or a warm desktop a claim took meanwhile, is never acted on.
	// The rights are asked when the menu first opens, and what an act changes
	// arrives on the live stream once DeskuBe writes it.
	let { row }: { row: DesktopRow } = $props();

	type Target = {
		namespace: string;
		name: string;
		desktop: Desktop;
		pool: string;
		claim?: DesktopClaim;
		who: string;
		own: boolean;
	};
	let target = $state.raw<Target | null>(null);
	let menu = $state<{ x: number; y: number; height: number } | null>(null);
	let confirming = $state<'release' | 'restart' | null>(null);
	const run = action({ toast: ui.toastError });
	const confirm = action();

	// The menu's footprint: under its button, or over it near the bottom.
	const MENU_W = 192;
	function open(e: MouseEvent) {
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		const d = row.desktop;
		const claim = row.claim;
		const height = d.pool ? 110 : 36;
		const below = r.bottom + 4 + height <= window.innerHeight;
		target = {
			namespace: d.namespace,
			name: d.name,
			desktop: d,
			pool: d.pool ?? '',
			claim,
			who: person(row),
			own: !!claim && claim.user === session.user?.username,
		};
		menu = { x: r.right - MENU_W, y: below ? r.bottom + 4 : r.top - 4 - height, height };
	}

	// What holds an act back before the caller's rights: a watch it reads
	// lost, or no claim to read. A restart reads no claim.
	const stale = $derived(new Set(desktops.value?.stale));
	const lost = (what: string) =>
		`The console lost its watch on the ${what}: acts wait until it is back.`;
	const claimHeld = $derived.by(() => {
		if (!target) return '';
		if (stale.has('vdiinstances')) return lost('desktops');
		if (stale.has('vdiclaims')) return lost('claims');
		if (target.claim) return '';
		return desktops.may?.lists(target.namespace, 'vdiclaims')
			? 'No one holds this desktop: its pool keeps it warm.'
			: 'Claims here are not shown to you.';
	});
	const power = $derived(target && (claimHeld || desktopGate.power(target).why));
	const release = $derived(target && (claimHeld || desktopGate.release(target).why));
	const restart = $derived.by(() => {
		if (!target) return '';
		if (stale.has('vdiinstances')) return lost('desktops');
		return restartHeld(target.desktop) || desktopGate.restart(target).why;
	});
	const stopping = $derived(target?.claim?.running !== false);

	function pick(fn: () => void) {
		menu = null;
		fn();
	}

	async function setPower(t: Target, start: boolean) {
		if (!t.claim) return;
		const { name: claim, uid } = t.claim;
		const done = await run.run(() =>
			api.desktopAct(start ? 'start' : 'stop', t.namespace, t.name, claim, uid),
		);
		if (done)
			ui.showToast(`${start ? 'Start' : 'Stop'} requested for ${t.name}.`, { kind: 'success' });
	}

	async function released(t: Target) {
		if (!t.claim) return;
		const { name: claim, uid } = t.claim;
		const done = await confirm.run(() =>
			api.desktopAct('release', t.namespace, t.name, claim, uid),
		);
		if (!done) return;
		confirming = null;
		ui.showToast(`Released ${t.name}: ${t.who}'s desktop and home are being deleted.`, {
			kind: 'success',
		});
	}

	async function restarted(t: Target) {
		const { uid, owner = '' } = t.desktop;
		const done = await confirm.run(() => api.desktopRestart(t.namespace, t.name, uid, owner));
		if (!done) return;
		confirming = null;
		ui.showToast(`Restart requested for ${t.name}.`, { kind: 'success' });
	}

	function close() {
		confirming = null;
		confirm.clear();
	}
</script>

<button
	onclick={open}
	disabled={run.busy}
	aria-label="Actions for {row.desktop.name}"
	aria-expanded={!!menu}
	class="flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
>
	Actions <ChevronDown size={12} />
</button>

{#if menu && target}
	{@const t = target}
	<ContextMenu x={menu.x} y={menu.y} height={menu.height} onclose={() => (menu = null)}>
		<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
			{#if t.pool}
				<MenuItem
					disabled={!!power}
					title={power || undefined}
					onclick={() => pick(() => setPower(t, !stopping))}
				>
					{stopping ? 'Stop' : 'Start'}
				</MenuItem>
			{/if}
			<MenuItem
				disabled={!!restart}
				title={restart || undefined}
				onclick={() => pick(() => (confirming = 'restart'))}
			>
				Restart...
			</MenuItem>
			{#if t.pool}
				<div class="my-1 border-t border-line-soft"></div>
				<MenuItem
					danger
					disabled={!!release}
					title={release || undefined}
					onclick={() => pick(() => (confirming = 'release'))}
				>
					Release...
				</MenuItem>
			{/if}
		</div>
	</ContextMenu>
{/if}

{#if confirming === 'release' && target?.claim}
	{@const t = target}
	<ConfirmDelete
		title="Release {t.name}"
		confirmWord={t.name}
		verb="Release"
		busy={confirm.busy}
		error={confirm.error}
		onconfirm={() => released(t)}
		onclose={close}
	>
		<div class="flex flex-col gap-3">
			<p>
				Deletes {t.who}'s claim <span class="font-mono">{t.claim?.name}</span> on pool {t.pool}.
			</p>
			<div class="flex flex-col gap-1.5">
				<Consequence tone="danger">
					{t.name} and its home, {t.who}'s files, are deleted for good.
				</Consequence>
				<Consequence tone="warn">{t.who}'s session, if one is open, ends.</Consequence>
				<Consequence>Runs now, under your account. It is not a pull request.</Consequence>
			</div>
		</div>
	</ConfirmDelete>
{:else if confirming === 'restart' && target}
	{@const t = target}
	<Modal title="Restart {t.name}?" onclose={close}>
		<div class="flex flex-col gap-1.5 px-5 py-4">
			<Consequence tone="warn">
				{t.who ? `${t.who}'s session` : 'Its session'}, if one is open, ends.
			</Consequence>
			<Consequence tone="info">A new pod starts. The home, and the files in it, stay.</Consequence>
			<Consequence>Runs now, under your account. It is not a pull request.</Consequence>
			<ErrorNote error={confirm.error} />
		</div>
		{#snippet footer()}
			<Button variant="secondary" class="ml-auto" onclick={close}>Cancel</Button>
			<Button onclick={() => restarted(t)} disabled={confirm.busy}>
				{confirm.busy ? 'Restarting...' : 'Restart'}
			</Button>
		{/snippet}
	</Modal>
{/if}
