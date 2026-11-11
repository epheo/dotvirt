<script lang="ts">
	import { untrack } from 'svelte';
	import type { StorageClass, StorageClassInfo, VM } from '#lib/api.js';
	import { liveMigrates } from '#lib/create.js';
	import { clusterGate } from '#lib/gates.js';
	import {
		pinnedHost,
		protectionChoices,
		roomText,
		tierRooms,
		type CellKind,
		type ProtectionClass,
	} from '#lib/replication.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { protectionCards, replication } from '#lib/state/replication.svelte.js';
	import CopyGlyph from '#lib/workspace/storage/CopyGlyph.svelte';
	import ClassPick from './ClassPick.svelte';
	import { LABEL } from './sheet.svelte.js';

	// Add disk's class choice. On r-drbd it asks how much the disk should
	// survive, one card per copy count, each landing on a class the caller may
	// list; other classes stay below. Anywhere else it is the class list.
	// value '' is the cluster default. bytes: the disk's size, for the room.
	// peers: the VM's other new disks; vm: the VM a disk is added to.
	let {
		disk,
		value,
		onpick,
		classes,
		bytes = 0,
		peers = [],
		vm,
	}: {
		disk: string;
		value: string;
		onpick: (name: string) => void;
		classes: (StorageClassInfo | StorageClass)[];
		bytes?: number;
		peers?: { storageClass: string; bytes: number }[];
		vm?: VM;
	} = $props();

	const id = $props.id();
	const cards = $derived(protectionCards(classes));
	const infos = $derived(classes.filter((c): c is StorageClassInfo => 'provisioner' in c));
	const others = $derived(classes.filter((c) => !('replication' in c) || !c.replication));

	// Room is placement data: only a caller who may read it gets numbers.
	const gate = $derived(clusterGate.replication());
	$effect(() => {
		if (!cards || !gate.ok || replication.overview) return;
		untrack(() => replication.pull(true));
	});
	const rooms = $derived(
		gate.ok && replication.overview?.synced ? tierRooms(replication.overview.pools) : null,
	);

	const choices = $derived(protectionChoices(infos, rooms));
	const current = $derived(value || inventory.defaultStorageClass);
	const picked = $derived(choices.find((ch) => ch.classes.some((c) => c.info.name === current)));
	const pickedClass = $derived(picked?.classes.find((c) => c.info.name === current));

	const pick = (c: ProtectionClass) => onpick(c.info.default ? '' : c.info.name);

	// One-copy disks of one VM share a host, so this one's room counts the
	// others in its tier, and the host one it already holds pins it to.
	const local = $derived.by(() => {
		if (!pickedClass?.hosts || picked?.copies !== 1) return undefined;
		const tier = pickedClass.tier;
		const ones = peers.flatMap((p) => {
			const r = infos.find(
				(i) => i.name === (p.storageClass || inventory.defaultStorageClass),
			)?.replication;
			return r?.copies === 1 && p.bytes > 0 ? [{ tier: r.tier, bytes: p.bytes }] : [];
		});
		const others = ones.filter((p) => p.tier === tier).map((p) => p.bytes);
		const tiers = [...new Set(ones.map((p) => p.tier).filter((t) => t !== tier))].map((t) => ({
			tier: t,
			disks: ones.filter((p) => p.tier === t).map((p) => p.bytes),
			hosts: rooms?.find((r) => r.tier === t)?.hosts ?? [],
		}));
		const volumes = replication.overview?.volumes;
		return { others, tiers, host: vm && volumes ? pinnedHost(vm, volumes) : undefined };
	});
	const GLYPHS: Record<number, CellKind[]> = {
		1: ['copy'],
		2: ['copy', 'copy', 'witness'],
		3: ['copy', 'copy', 'copy'],
	};
</script>

{#if !cards}
	<ClassPick label="{disk} class" {value} {onpick} {classes} />
{:else}
	<fieldset class="flex min-w-0 flex-col gap-2">
		<legend class="mb-1.5 {LABEL}">{disk}: how much should it survive?</legend>
		<div class="grid grid-cols-[repeat(auto-fit,minmax(min(150px,100%),1fr))] gap-2">
			{#each choices as ch (ch.copies)}
				{@const on = picked?.copies === ch.copies}
				{@const none = !ch.classes.length}
				<label
					class="flex min-w-0 flex-col gap-1.5 rounded-md border px-3 py-2.5 {on
						? 'border-accent bg-select-soft'
						: 'border-line'} {none ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-inset'}"
				>
					<span class="flex items-center gap-2">
						<input
							type="radio"
							name={id}
							checked={on}
							disabled={none}
							onchange={() => pick(ch.classes[0])}
							class="size-4 accent-accent"
						/>
						<span class="text-sm font-medium {none ? 'text-ink-muted' : 'text-ink'}"
							>{ch.title}</span
						>
						<span class="ml-auto flex gap-0.5" aria-hidden="true"
							>{#each GLYPHS[ch.copies] as kind, i (i)}<CopyGlyph {kind} />{/each}</span
						>
					</span>
					{#each ch.lines as line (line)}
						<span class="text-xs leading-4 text-ink-muted">{line}</span>
					{/each}
					{#if none}
						<span class="text-xs leading-4 font-medium text-ink-soft"
							>No class you can use keeps {ch.copies === 1 ? 'one copy' : `${ch.copies} copies`}.
							Ask your platform team to add one.</span
						>
					{/if}
				</label>
			{/each}
		</div>

		{#if picked && picked.classes.length > 1}
			<div class="flex flex-col overflow-hidden rounded-md border border-line">
				{#each picked.classes as c (c.info.name)}
					<label
						class="flex cursor-pointer items-center gap-3 border-t border-line-soft px-3 py-2 first:border-t-0 {c
							.info.name === current
							? 'bg-select-soft'
							: 'hover:bg-inset'}"
					>
						<input
							type="radio"
							name="{id}-class"
							checked={c.info.name === current}
							onchange={() => pick(c)}
							class="size-4 accent-accent"
						/>
						<span class="font-mono text-sm font-medium text-ink">{c.info.name}</span>
						<span class="text-xs text-ink-muted">{c.tier}</span>
					</label>
				{/each}
			</div>
		{/if}

		{#if pickedClass && picked}
			{@const room = roomText(pickedClass, picked.copies, bytes, local)}
			<div class="flex flex-col gap-1">
				<span class="text-xs text-ink-muted"
					>Class <span class="font-mono">{pickedClass.info.name}</span>, tier {pickedClass.tier}.</span
				>
				{#if picked.copies > 1}
					<span class="flex items-center gap-1.5 text-xs text-ink-muted">
						<span
							class="size-[7px] rounded-full {liveMigrates(pickedClass.info) ? 'bg-ok' : 'bg-warn'}"
						></span>
						{liveMigrates(pickedClass.info)
							? 'Shared across hosts: live migration possible'
							: 'Stays on one host: no live migration'}
					</span>
				{/if}
				{#if room}
					<span class="flex items-center gap-1.5 text-xs text-ink-muted">
						<span class="size-[7px] rounded-full {room.ok ? 'bg-ok' : 'bg-warn'}"></span>
						{room.text}
					</span>
				{/if}
				<span class="text-xs text-ink-faint"
					>Each copy lands on its own host when the disk is created. r-drbd never moves a copy on
					its own.</span
				>
			</div>
		{/if}

		{#if others.length}
			<ClassPick label="Other classes" {value} {onpick} classes={others} />
		{/if}
	</fieldset>
{/if}
