<script lang="ts">
	import { ChevronDown } from '@lucide/svelte';
	import { api, type DesktopClaim } from '#lib/api.js';
	import { person, type DesktopRow } from '#lib/desktops.js';
	import { desktopGate } from '#lib/gates.js';
	import { action } from '#lib/resource.svelte.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { session } from '#lib/state/session.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import ConfirmDelete from '#lib/components/ConfirmDelete.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import ContextMenu from '#lib/components/ContextMenu.svelte';
	import MenuItem from '#lib/components/MenuItem.svelte';

	// The acts on a pool's desktop, run now under the caller's account on the
	// claim holding it. The menu, and the dialog it opens, act on the claim
	// shown when the menu opened, named by its UID: a claim deleted since, and
	// another made under its name, is never acted on. The rights are asked when
	// the menu first opens, and what an act changes arrives on the live stream
	// once DeskuBe writes it.
	let { row }: { row: DesktopRow } = $props();

	type Target = {
		namespace: string;
		name: string;
		pool: string;
		claim?: DesktopClaim;
		who: string;
		own: boolean;
	};
	let target = $state.raw<Target | null>(null);
	let menu = $state<{ x: number; y: number } | null>(null);
	let releasing = $state(false);
	const run = action({ toast: ui.toastError });
	const confirm = action();

	// The menu's footprint: under its button, or over it near the bottom.
	const MENU_W = 192;
	const MENU_H = 80;
	function open(e: MouseEvent) {
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		const below = r.bottom + 4 + MENU_H <= window.innerHeight;
		const claim = row.claim;
		target = {
			namespace: row.desktop.namespace,
			name: row.desktop.name,
			pool: row.desktop.pool ?? '',
			claim,
			who: person(row),
			own: !!claim && claim.user === session.user?.username,
		};
		menu = { x: r.right - MENU_W, y: below ? r.bottom + 4 : r.top - 4 - MENU_H };
	}

	// What holds an act back before the caller's rights.
	const held = $derived.by(() => {
		if (!target) return '';
		if (desktops.value?.stale)
			return 'The console lost its watch on the desktops: acts wait until it is back.';
		if (target.claim) return '';
		return desktops.may?.lists(target.namespace, 'vdiclaims')
			? 'No one holds this desktop: its pool keeps it warm.'
			: 'Claims here are not shown to you.';
	});
	const power = $derived(
		target && (held || desktopGate.power(target.namespace, target.pool, target.own).why),
	);
	const release = $derived(
		target && (held || desktopGate.release(target.namespace, target.pool, target.own).why),
	);
	const stopping = $derived(target?.claim?.running !== false);

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
		releasing = false;
		ui.showToast(`Released ${t.name}: ${t.who}'s desktop and home are being deleted.`, {
			kind: 'success',
		});
	}

	function close() {
		releasing = false;
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
	<ContextMenu x={menu.x} y={menu.y} height={MENU_H} onclose={() => (menu = null)}>
		<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
			<MenuItem
				disabled={!!power}
				title={power || undefined}
				onclick={() => {
					menu = null;
					setPower(t, !stopping);
				}}
			>
				{stopping ? 'Stop' : 'Start'}
			</MenuItem>
			<div class="my-1 border-t border-line-soft"></div>
			<MenuItem
				danger
				disabled={!!release}
				title={release || undefined}
				onclick={() => {
					menu = null;
					releasing = true;
				}}
			>
				Release...
			</MenuItem>
		</div>
	</ContextMenu>
{/if}

{#if releasing && target?.claim}
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
{/if}
