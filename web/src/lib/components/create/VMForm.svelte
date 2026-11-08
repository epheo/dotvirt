<script lang="ts">
	import { untrack } from 'svelte';
	import { Check, CircleCheck, Copy, TriangleAlert, X } from '@lucide/svelte';
	import { api, type CreateVMRequest, type Network, type StorageClassInfo } from '#lib/api.js';
	import {
		defaultUser,
		generatePassword,
		imageTally,
		liveMigrates,
		matchRanges,
		mostCommonSize,
		networkLine,
		nextFreeName,
		preferenceFor,
		searchNetworks,
		sharedClass,
		sizeLabel,
		sizeTally,
		usualNetworks,
		usualPreference,
		vmCountByNetwork,
	} from '#lib/create.js';
	import { bytes, plural, quantity, quantityBytes } from '#lib/format.js';
	import { placement } from '#lib/headroom.js';
	import { attachRef, attachableNetworks } from '#lib/networks.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { rowList, type RowList } from '#lib/rowlist.svelte.js';
	import { diskNameError, freeName, RESERVED_DISKS } from '#lib/devices.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { memoryRoom } from '#lib/state/hosts.svelte.js';
	import { vmGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Card from '../Card.svelte';
	import Consequence from '../Consequence.svelte';
	import Button from '../Button.svelte';
	import ClassPick from './ClassPick.svelte';
	import PickList from './PickList.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SetupRow from './SetupRow.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, useSheet } from './sheet.svelte.js';

	// New VM: a name and a place, everything else set up from what the
	// project usually runs, each choice one line with Change. Full parity with
	// the former wizard sits behind the Change editors: every instancetype,
	// preference and image, extra disks, the default network toggle, guest
	// login and the one-time console password reveal.
	let {
		namespace = $bindable(),
		namespaces,
		image,
		onclose,
	}: {
		namespace: string;
		namespaces: string[] | null; // null = every namespace the caller may stage into
		image?: string; // "namespace/name"
		onclose: () => void;
	} = $props();

	const sheet = useSheet();
	inventory.loadOptions();
	const options = $derived(inventory.options);
	// Every place the caller may stage into, the opener's project first; an
	// opener that narrowed the list (a project's "New VM here") picks the start.
	const places = $derived(inventory.namespaces);
	// svelte-ignore state_referenced_locally
	const opener = inventory.projectOf(namespace || namespaces?.[0] || '');
	$effect(() => {
		const list = places;
		untrack(() => {
			if (!list.includes(namespace)) namespace = namespaces?.[0] ?? list[0] ?? '';
		});
	});

	const project = $derived(inventory.projectOf(namespace));
	const projectVMs = $derived(
		inventory.projects.find((p) => p.name === project)?.namespaces.flatMap((n) => n.vms) ?? [],
	);
	const nsVMs = $derived(projectVMs.filter((v) => v.namespace === namespace));

	// ── name ───────────────────────────────────────────────────────────────
	const stagedNames = $derived(
		drafts.drafts.flatMap((d) =>
			d.draft.items
				.filter((i) => i.kind === 'create' && i.namespace === namespace && !i.resource)
				.map((i) => i.name),
		),
	);
	const taken = $derived(new Set([...nsVMs.map((v) => v.name), ...stagedNames]));
	const next = $derived(nextFreeName(taken, namespace || 'vm'));
	let name = $state('');
	let nameTouched = false;
	$effect(() => {
		const n = next.name;
		untrack(() => {
			if (!nameTouched) name = n;
		});
	});
	const nameHelp = $derived.by(() => {
		if (!name) return { ok: false, text: `A name is required. ${next.name} is free.` };
		if (!validName(name)) return { ok: false, text: NAME_HINT };
		if (taken.has(name)) return { ok: false, text: `Taken in ${namespace}. ${next.name} is free.` };
		if (name === next.name && next.after)
			return { ok: true, text: `Free. Next after ${next.after}.` };
		return { ok: true, text: 'Free.' };
	});

	// ── size ───────────────────────────────────────────────────────────────
	const tally = $derived(sizeTally(projectVMs, options));
	const common = $derived(mostCommonSize(projectVMs, options));
	let instancetype = $state('');
	let sizeTouched = false;
	$effect(() => {
		const its = options?.instancetypes ?? [];
		const pick = common?.it.name ?? its.find((i) => i.name === 'u1.medium')?.name ?? its[0]?.name;
		untrack(() => {
			if (!sizeTouched && pick) instancetype = pick;
		});
	});
	const it = $derived(options?.instancetypes?.find((i) => i.name === instancetype));
	const sizeWhy = $derived(
		common && instancetype === common.it.name
			? `Most common in ${project}`
			: tally.get(instancetype)
				? `${plural(tally.get(instancetype) ?? 0, 'VM')} in ${project} ${tally.get(instancetype) === 1 ? 'uses' : 'use'} it`
				: sizeTouched
					? 'Your pick'
					: 'The default size',
	);
	let sizeQuery = $state('');
	const sizes = $derived(
		(options?.instancetypes ?? [])
			.filter(
				(i) =>
					!sizeQuery ||
					`${i.name} ${sizeLabel(i)}`.toLowerCase().includes(sizeQuery.trim().toLowerCase()),
			)
			.sort((a, b) => (tally.get(b.name) ?? 0) - (tally.get(a.name) ?? 0)),
	);

	// ── boot image and OS tuning ───────────────────────────────────────────
	const ready = $derived((options?.osImages ?? []).filter((i) => i.ready));
	const refOf = (i: { name: string; namespace: string }) => `${i.name}|${i.namespace}`;
	// Disks per image across the fleet: the most used image is the default,
	// and the picker leads with the ones in use.
	const used = $derived(imageTally(inventory.allVMs));
	const usedBy = (i: { name: string; namespace: string }) =>
		used.get(`${i.namespace}/${i.name}`) ?? 0;
	const usualPref = $derived(usualPreference(projectVMs));
	// svelte-ignore state_referenced_locally
	let osImage = $state(image ? image.split('/').reverse().join('|') : '');
	// svelte-ignore state_referenced_locally
	let imageTouched = !!image;
	$effect(() => {
		const list = ready;
		const prefs = options?.preferences ?? [];
		const usual = usualPref;
		const most = [...list].sort((a, b) => usedBy(b) - usedBy(a))[0];
		untrack(() => {
			if (imageTouched || !list.length) return;
			const like =
				most && usedBy(most) > 0
					? most
					: usual
						? list.find((i) => preferenceFor(i.name, prefs)?.name === usual)
						: undefined;
			const pick = like ?? list.find((i) => i.name.startsWith('fedora')) ?? list[0];
			if (pick) osImage = refOf(pick);
		});
	});
	const imageName = $derived(osImage.split('|')[0]);
	const picked = $derived(ready.find((i) => refOf(i) === osImage));
	const imageWhy = $derived.by(() => {
		if (image && osImage === image.split('/').reverse().join('|')) return 'The image you picked';
		const n = picked ? usedBy(picked) : 0;
		const top = ready.every((i) => usedBy(i) <= n);
		if (n && top) return `Most used of ${plural(ready.length, 'image')}, on ${plural(n, 'disk')}`;
		if (n) return `On ${plural(n, 'disk')}`;
		if (usualPref && preferenceFor(imageName, options?.preferences ?? [])?.name === usualPref)
			return `Like most VMs in ${project}`;
		return `Ready to boot, one of ${plural(ready.length, 'image')}`;
	});
	let preference = $state('');
	let prefTouched = false;
	$effect(() => {
		const prefs = options?.preferences ?? [];
		const img = imageName;
		untrack(() => {
			if (prefTouched || !prefs.length) return;
			preference =
				preferenceFor(img, prefs)?.name ??
				prefs.find((p) => p.name === 'fedora')?.name ??
				prefs[0].name;
		});
	});
	let imageQuery = $state('');
	const images = $derived(
		(options?.osImages ?? [])
			.filter((i) => !imageQuery || i.name.toLowerCase().includes(imageQuery.trim().toLowerCase()))
			.sort((a, b) => usedBy(b) - usedBy(a) || a.name.localeCompare(b.name)),
	);

	// ── disks ──────────────────────────────────────────────────────────────
	let diskSize = $state('30Gi');
	let storageClass = $state(''); // '' = the cluster default
	// A new disk takes the first name the VM does not use yet: after a removal
	// the gap is filled, never a repeat the server would refuse.
	type ExtraDisk = { name: string; size: string; storageClass: string };
	const disks: RowList<ExtraDisk> = rowList<ExtraDisk>(
		(): ExtraDisk => ({
			name: freeName('disk', [...RESERVED_DISKS, ...disks.rows.map((d) => d.name)]),
			size: '10Gi',
			storageClass: '',
		}),
		[],
		{ empty: true },
	);
	const diskError = (i: number) =>
		diskNameError(disks.rows[i].name, [
			...RESERVED_DISKS,
			...disks.rows.filter((_, j) => j !== i).map((d) => d.name),
		]);
	const classesRes = resource<StorageClassInfo[]>(() => 'storage-classes', api.storageClasses);
	const classes = $derived(classesRes.data ?? []);
	// Facts when the class list loaded, else the bare names the options carry.
	const classRows = $derived(classes.length ? classes : (options?.storageClasses ?? []));
	const classOf = (c: string) =>
		classes.find((x) => x.name === (c || inventory.defaultStorageClass));
	const rootClass = $derived(classOf(storageClass));
	const rootClassName = $derived(
		storageClass || inventory.defaultStorageClass || 'the default class',
	);
	// Every disk on a class that follows the VM, or nothing is known.
	const pinned = $derived(
		[storageClass, ...disks.rows.map((d) => d.storageClass)].some((c) => {
			const info = classOf(c);
			return info && !liveMigrates(info);
		}),
	);
	const keep = $derived(pinned ? sharedClass(classes) : undefined);
	function keepMigration() {
		if (!keep) return;
		storageClass = keep.name;
		for (const d of disks.rows) d.storageClass = keep.name;
	}
	const sizeOK = (q: string) => /^\d+(\.\d+)?(Mi|Gi|Ti)$/.test(q);

	// ── networks ───────────────────────────────────────────────────────────
	const available = $derived(attachableNetworks(inventory.networks, namespace));
	const primaryNet = $derived(
		inventory.networks.find((n) => n.kind === 'default' && n.namespace === namespace),
	);
	const primaryLabel = $derived(primaryNet?.name ?? 'Pod network');
	let selectedNets = $state<string[]>([]);
	let attachPrimary = $state(true);
	let netTouched = $state(false);
	// A project network belongs to its namespace: a new place resets the
	// picks to what that namespace's VMs usually attach.
	$effect(() => {
		const usual = usualNetworks(nsVMs, available);
		const ns = namespace;
		untrack(() => {
			void ns;
			if (netTouched) return;
			selectedNets = usual;
			attachPrimary = true;
		});
	});
	$effect(() => {
		namespace;
		untrack(() => (netTouched = false));
	});
	const hasNetwork = $derived(attachPrimary || selectedNets.length > 0);
	const counts = $derived(vmCountByNetwork(inventory.allVMs, inventory.networks));
	let netQuery = $state('');
	const hits = $derived(
		searchNetworks(available, netQuery, {
			project,
			projectOf: (ns) => inventory.projectOf(ns),
			counts,
		}),
	);
	const netByRef = (ref: string) => available.find((n) => attachRef(n) === ref);
	const attached = $derived([
		...(attachPrimary ? [primaryLabel] : []),
		...selectedNets.map((r) => netByRef(r)?.name ?? r),
	]);
	// The row names the networks a VM is put on; the plain pod network every
	// VM keeps by default goes unnamed unless it is the only one.
	const named = $derived(
		selectedNets.length
			? [
					...(attachPrimary && primaryNet ? [primaryNet.name] : []),
					...attached.slice(attachPrimary ? 1 : 0),
				]
			: attachPrimary
				? [primaryNet?.name ?? 'The default network']
				: [],
	);
	const netWhy = $derived(
		netTouched
			? attachPrimary && !primaryNet && selectedNets.length
				? 'Plus the default network'
				: !attachPrimary
					? 'No default network'
					: ''
			: `Same as most VMs in ${namespace}`,
	);
	const addressed = $derived(
		[
			...(attachPrimary && primaryNet?.subnets?.length ? [primaryNet] : []),
			...selectedNets.map(netByRef).filter((n): n is Network => !!n?.subnets?.length),
		].map((n) => n.name),
	);
	function toggleNet(n: Network, on: boolean) {
		netTouched = true;
		const ref = attachRef(n);
		selectedNets = on ? [...selectedNets, ref] : selectedNets.filter((r) => r !== ref);
	}

	// ── access ─────────────────────────────────────────────────────────────
	// The key from the last VM made here, a per-browser convenience.
	const lastKey = persisted('dotvirt.create.sshKey', '');
	let sshKey = $state(lastKey.value);
	let user = $state('');
	let userAuto = '';
	$effect(() => {
		const auto = defaultUser(imageName);
		untrack(() => {
			if (user === '' || user === userAuto) user = auto;
			userAuto = auto;
		});
	});
	// Cloud images ship with locked passwords, so every VM gets a console one.
	// Git keeps only its bcrypt hash: the plaintext lives here and in the
	// reveal after staging, then nowhere.
	let password = $state(generatePassword());

	let running = $state(true);

	// ── placement ──────────────────────────────────────────────────────────
	const room = memoryRoom(() => true);
	const fit = $derived.by(() => {
		const r = room.data;
		const need = quantityBytes(it?.memory ?? '');
		return r?.cap && need > 0 ? placement(r.cap, r.nodes, need) : null;
	});

	const fitLine = $derived.by((): { tone: 'ok' | 'warn'; text: string } | null => {
		if (!fit) return null;
		if (fit.fits > 0) {
			const left =
				fit.excluded.length === 1
					? ` ${fit.excluded[0].node} is ${fit.excluded[0].reason}.`
					: fit.excluded.length
						? ` ${fit.excluded.length} take no new VMs.`
						: '';
			const n =
				fit.fits !== fit.total
					? `${fit.fits} of ${plural(fit.total, 'host')}`
					: fit.total === 1
						? 'the only host'
						: `all ${fit.total} hosts`;
			return { tone: 'ok', text: `Fits on ${n}.${left}` };
		}
		const most = fit.best ? ` Most room: ${fit.best.node}, ${bytes(fit.best.free)}.` : '';
		return { tone: 'warn', text: `No host has ${quantity(it?.memory ?? '')} free.${most}` };
	});

	// ── the request ────────────────────────────────────────────────────────
	// A size under the OS tuning's minimum fails KubeVirt's webhook at sync
	// time, so it is refused here, before staging.
	const short = $derived.by(() => {
		const p = options?.preferences?.find((x) => x.name === preference);
		if (!p || !it) return '';
		const cpuLow = p.minCPU && it.cpu < p.minCPU;
		const memLow = p.minMemory && quantityBytes(it.memory) < quantityBytes(p.minMemory);
		return cpuLow || memLow
			? `${p.displayName || p.name} needs at least ${p.minCPU ?? it.cpu} vCPU ${quantity(p.minMemory ?? it.memory)}`
			: '';
	});

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name the VM');
		else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
		else if (taken.has(name)) m.push(`${name} is taken in ${namespace}`);
		if (!namespace) m.push('Pick a project and namespace');
		else if (!vmGate.create(namespace).ok) m.push(vmGate.create(namespace).why);
		if (!osImage) m.push('Pick an image to boot from');
		if (!instancetype) m.push('Pick a size');
		if (!preference) m.push('Pick an OS tuning');
		else if (short) m.push(short);
		if (!sizeOK(diskSize)) m.push('The disk size needs a unit, like 30Gi');
		if (!hasNetwork) m.push('Keep at least one network');
		for (const e of disks.rows.map((_, i) => diskError(i))) if (e) m.push(e);
		return m;
	});

	const request = $derived.by((): CreateVMRequest => {
		const [imgName, imgNs] = osImage.split('|');
		const req: CreateVMRequest = {
			name,
			namespace,
			instancetype,
			preference,
			osImage: { name: imgName, namespace: imgNs },
			diskSize,
			storageClass: storageClass || undefined,
			running,
			extraDisks: disks.rows.length
				? disks.rows.map((d) => ({
						name: d.name,
						size: d.size,
						storageClass: d.storageClass || undefined,
					}))
				: undefined,
			networks: selectedNets.length ? selectedNets.map((n) => ({ name: n })) : undefined,
			primaryNetwork: attachPrimary,
		};
		if (user || sshKey || password)
			req.cloudInit = {
				user: user || undefined,
				sshKey: sshKey.trim() || undefined,
				password: password || undefined,
			};
		return req;
	});

	const op = action();
	let revealed = $state(false);
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		if (await op.run(() => api.stageCreate(req))) {
			if (sshKey.trim()) lastKey.value = sshKey.trim();
			ui.toastStaged();
			if (password) {
				revealed = true;
				sheet.pinned = true;
			} else onclose();
		}
	}
	sheet.submit = stage;

	let open = $state<'' | 'size' | 'image' | 'disk' | 'network' | 'access'>('');
	const close = () => (open = '');

	let copied = $state('');
	function copy(what: string, value: string) {
		navigator.clipboard.writeText(value);
		copied = what;
		setTimeout(() => (copied = ''), 1500);
	}
</script>

{#snippet marked(text: string, q: string)}
	{@const ranges = matchRanges(text, q)}
	{#if !ranges.length}{text}{:else}{#each ranges as r, i (r.start)}{text.slice(
				i ? ranges[i - 1].end : 0,
				r.start,
			)}<mark class="rounded-sm bg-select text-inherit">{text.slice(r.start, r.end)}</mark
			>{/each}{text.slice(ranges[ranges.length - 1].end)}{/if}
{/snippet}

{#if revealed}
	<SheetBody>
		{#snippet form()}
			<p class="text-sm text-ink-soft">
				<span class="font-mono font-medium">{name}</span> is staged. Copy its console password now.
			</p>
			<dl class="flex flex-col rounded-lg border border-line">
				{#each [{ k: 'User', v: user || 'image default' }, { k: 'Password', v: password }] as row (row.k)}
					<div
						class="flex items-center justify-between gap-3 border-t border-line-soft px-4 py-2.5 first:border-t-0"
					>
						<dt class="text-[13px] text-ink-muted">{row.k}</dt>
						<dd class="flex items-center gap-2 font-mono text-sm text-ink">
							{row.v}
							<button
								type="button"
								onclick={() => copy(row.k, row.v)}
								aria-label="Copy {row.k.toLowerCase()}"
								class="text-ink-faint hover:text-ink-soft"
								>{#if copied === row.k}<Check size={14} />{:else}<Copy size={14} />{/if}</button
							>
						</dd>
					</div>
				{/each}
			</dl>
			<p
				class="rounded-lg border border-warn/50 bg-warn-soft px-3.5 py-2.5 text-[13px] text-warn-ink"
			>
				This is the only time the password shows. Git keeps a hash, so it cannot be read back. If it
				is lost, stage a new one from the VM's settings.
			</p>
		{/snippet}
		{#snippet footer()}
			<span class="flex-1 text-ink-muted">It is created when the pull request merges.</span>
			<Button class="h-9! px-4!" onclick={onclose} data-autofocus>Done</Button>
		{/snippet}
	</SheetBody>
{:else}
	<SheetBody error={op.error || inventory.optionsError}>
		{#snippet form()}
			<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="vm-name">Name</label>
					<input
						id="vm-name"
						class="{FIELD} font-mono font-medium"
						bind:value={name}
						oninput={() => (nameTouched = true)}
						autocomplete="off"
						spellcheck="false"
						data-autofocus
						aria-describedby="vm-name-help"
					/>
					<span id="vm-name-help" class="{HELP} {nameHelp.ok ? 'text-ok-ink!' : 'text-warn-ink!'}">
						{#if nameHelp.ok}<Check size={13} strokeWidth={2.4} />{:else}<TriangleAlert
								size={13}
							/>{/if}{nameHelp.text}
					</span>
				</div>
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="vm-where">Project and namespace</label>
					<PlacePicker id="vm-where" bind:value={namespace} namespaces={places} first={opener} />
					<span class={HELP}>
						{namespace && inventory.projectOf(namespace) === opener ? 'Where you were. ' : ''}Search
						{plural(new Set(places.map((n) => inventory.projectOf(n))).size, 'project')}{opener
							? ', yours first'
							: ''}.
					</span>
				</div>
			</div>

			{#if !options}
				<p class="text-sm text-ink-faint">
					{inventory.optionsError ? 'Cluster options did not load.' : 'Loading cluster options...'}
				</p>
			{:else}
				<Card title="Set up for you" flush>
					{#snippet actions()}<span class="text-ink-faint">From {project}'s usual choices</span
						>{/snippet}
					<div class="flex flex-col">
						<SetupRow
							label="Size"
							open={open === 'size'}
							onopen={() => (open = 'size')}
							onclose={close}
						>
							{#snippet value()}{it ? sizeLabel(it) : 'Pick a size'}{/snippet}
							{#snippet sub()}{sizeWhy}{/snippet}
							{#snippet editor(done)}
								<PickList
									label="Sizes"
									bind:query={sizeQuery}
									search={(options?.instancetypes.length ?? 0) > 8}
									items={sizes}
									key={(i) => i.name}
									selected={(i) => i.name === instancetype}
									ontoggle={(i) => {
										sizeTouched = true;
										instancetype = i.name;
									}}
									{done}
									note={sizeQuery
										? `${sizes.length} of ${options?.instancetypes.length} sizes match.`
										: `${project}'s most used come first.`}
								>
									{#snippet row(i)}
										<span class="flex items-baseline justify-between gap-3">
											<span class="text-sm text-ink">{sizeLabel(i)}</span>
											{#if tally.get(i.name)}<span class="text-[13px] text-ink-soft tabular-nums"
													>{tally.get(i.name)} VMs</span
												>{/if}
										</span>
										<span class="font-mono text-xs text-ink-muted">{i.name}</span>
									{/snippet}
								</PickList>
							{/snippet}
						</SetupRow>

						<SetupRow
							label="Boots from"
							open={open === 'image'}
							onopen={() => (open = 'image')}
							onclose={close}
						>
							{#snippet value()}<span class="font-mono font-medium"
									>{imageName || 'Pick an image'}</span
								>{/snippet}
							{#snippet sub()}{imageWhy}{/snippet}
							{#snippet editor(done)}
								<PickList
									label="Boot images"
									bind:query={imageQuery}
									items={images}
									key={refOf}
									selected={(i) => refOf(i) === osImage}
									disabled={(i) => !i.ready}
									ontoggle={(i) => {
										imageTouched = true;
										osImage = refOf(i);
									}}
									{done}
									note={imageQuery
										? `${images.length} of ${plural(options?.osImages.length ?? 0, 'image')} match.`
										: `${plural(ready.length, 'image')} ready to boot.`}
								>
									{#snippet row(i)}
										<span class="flex items-baseline justify-between gap-3">
											<span class="font-mono text-sm font-medium text-ink"
												>{@render marked(i.name, imageQuery)}</span
											>
											{#if !i.ready}<span class="text-xs text-ink-faint">importing</span
												>{:else if usedBy(i)}<span
													class="text-[13px] whitespace-nowrap text-ink-soft tabular-nums"
													>{plural(usedBy(i), 'disk')}</span
												>{/if}
										</span>
										<span class="text-xs text-ink-muted">in {i.namespace}</span>
									{/snippet}
									{#snippet after()}
										<div class="flex flex-col gap-1.5">
											<label class={LABEL} for="vm-pref">OS tuning</label>
											<select
												id="vm-pref"
												class={FIELD}
												bind:value={preference}
												onchange={() => (prefTouched = true)}
											>
												{#each options?.preferences ?? [] as p (p.name)}
													<option value={p.name}>{p.displayName || p.name}</option>
												{/each}
											</select>
											<span class={HELP}>Matched to the image; it tunes devices and firmware.</span>
										</div>
									{/snippet}
								</PickList>
							{/snippet}
						</SetupRow>

						<SetupRow
							label="Disk"
							open={open === 'disk'}
							onopen={() => (open = 'disk')}
							onclose={close}
						>
							{#snippet value()}{quantity(diskSize)} on
								<span class="font-mono font-medium">{rootClassName}</span>{#if disks.rows.length},
									plus {disks.rows.length} more{/if}{/snippet}
							{#snippet sub()}
								{#if rootClass}
									<span
										class="size-[7px] shrink-0 rounded-full {liveMigrates(rootClass)
											? 'bg-ok'
											: 'bg-warn'}"
									></span>
									{liveMigrates(rootClass)
										? 'Moves with the VM: shared across hosts'
										: 'Cannot live-migrate: stays on one host'}
								{:else}Disks the VM keeps across restarts{/if}
							{/snippet}
							{#snippet editor(done)}
								{@render diskEditor(done)}
							{/snippet}
						</SetupRow>

						<SetupRow
							label="Network"
							open={open === 'network'}
							onopen={() => (open = 'network')}
							onclose={close}
						>
							{#snippet value()}
								{#if named.length && (selectedNets.length || primaryNet)}<span
										class="font-mono font-medium">{named.join(', ')}</span
									>{:else}{named[0] ?? 'None'}{/if}
							{/snippet}
							{#snippet sub()}
								{#if !hasNetwork}<TriangleAlert size={13} class="text-warn-ink" />A VM needs a
									network{:else}{netWhy}{/if}
							{/snippet}
							{#snippet editor(done)}
								<PickList
									label="Networks to attach"
									bind:query={netQuery}
									multiple
									items={hits}
									key={(h) => attachRef(h.net)}
									selected={(h) => selectedNets.includes(attachRef(h.net))}
									ontoggle={(h, on) => toggleNet(h.net, on)}
									{done}
									note={netQuery
										? `${hits.length} of ${plural(available.length, 'network')} match. ${project}'s come first.`
										: available.length
											? `${plural(available.length, 'network')} you can attach in ${namespace}. ${project}'s come first.`
											: `No other network reaches ${namespace}.`}
								>
									{#snippet row(h)}
										<span class="flex items-baseline justify-between gap-3">
											<span class="font-mono text-sm font-medium text-ink"
												>{@render marked(h.net.name, netQuery)}</span
											>
											<span class="text-[13px] whitespace-nowrap text-ink-soft tabular-nums"
												>{h.vms.toLocaleString()} VMs</span
											>
										</span>
										<span class="block text-xs leading-4 text-ink-muted"
											>{@render marked(
												networkLine(h.net, (ns) => inventory.projectOf(ns)),
												netQuery,
											)}{#if h.net.subnets?.length}, <span class="font-mono"
													>{h.net.subnets[0]}</span
												>{/if}</span
										>
									{/snippet}
									{#snippet after()}
										<label class="flex items-center gap-2 text-xs text-ink-soft">
											<input
												type="checkbox"
												class="size-4 accent-accent"
												bind:checked={attachPrimary}
												onchange={() => (netTouched = true)}
											/>
											Also keep the default network,
											<span class="font-mono font-medium">{primaryLabel}</span>
										</label>
										{#if !hasNetwork}
											<p class="text-xs text-warn-ink">Keep one network: a VM needs a NIC.</p>
										{/if}
									{/snippet}
								</PickList>
							{/snippet}
						</SetupRow>

						<SetupRow
							label="Access"
							open={open === 'access'}
							onopen={() => (open = 'access')}
							onclose={close}
						>
							{#snippet value()}
								{#if sshKey.trim()}Your SSH key, <span class="font-mono font-medium">{user}</span
									>{:else}Console password, <span class="font-mono font-medium">{user}</span>{/if}
							{/snippet}
							{#snippet sub()}
								{#if sshKey.trim()}{sshKey.trim() === lastKey.value
										? 'The one on your last VM'
										: 'Added here'}{password
										? ', plus a console password'
										: ''}{:else if password}Shown once after staging{:else}No way in yet: add a key
									or a password{/if}
							{/snippet}
							{#snippet editor(done)}
								{@render accessEditor(done)}
							{/snippet}
						</SetupRow>
					</div>
				</Card>
			{/if}
		{/snippet}

		{#snippet aside()}
			<StagesPanel
				path="{namespace}/{name || '<name>'}.yaml"
				{project}
				preview={missing.length ? null : { kind: 'vm', body: request }}
			>
				{#snippet consequences()}
					{#if fitLine}
						<Consequence tone={fitLine.tone}>{fitLine.text}</Consequence>
					{/if}
					{#if pinned}
						<Consequence tone="warn"
							>{name || 'The VM'} cannot live-migrate.{#if keep}
								<button
									type="button"
									class="text-accent-ink hover:underline"
									onclick={keepMigration}>Use {keep.name} to keep it</button
								>{/if}</Consequence
						>
					{:else if rootClass}
						<Consequence tone="ok">{name || 'The VM'} can live-migrate.</Consequence>
					{/if}
					{#if addressed.length}
						<Consequence tone="info"
							>Starts with an address on {addressed.join(' and ')}.</Consequence
						>
					{:else if hasNetwork}
						<Consequence tone="info">Starts on {attached.join(' and ')}.</Consequence>
					{/if}
					{#if !running}
						<Consequence>Stays stopped until you start it.</Consequence>
					{/if}
				{/snippet}
			</StagesPanel>
		{/snippet}

		{#snippet footer()}
			<SheetFooter label="Stage VM" {missing} busy={op.busy} onsubmit={stage} oncancel={onclose}>
				{#snippet left()}
					<label class="flex cursor-pointer items-center gap-2">
						<input type="checkbox" class="size-4 accent-accent" bind:checked={running} />
						Start {name || 'it'} when it merges
					</label>
				{/snippet}
			</SheetFooter>
		{/snippet}
	</SheetBody>
{/if}

{#snippet diskEditor(done: () => void)}
	<div class="flex flex-col gap-3">
		<div class="flex items-end gap-2">
			<div class="flex w-28 flex-col gap-1.5">
				<label class={LABEL} for="vm-disk">Size</label>
				<input id="vm-disk" class="{FIELD} font-mono" bind:value={diskSize} />
			</div>
			<span class="flex-1"></span>
			<button
				type="button"
				onclick={done}
				class="h-9 rounded-md border border-line bg-panel px-3 text-sm text-ink hover:border-ink-faint"
				>Done</button
			>
		</div>
		<ClassPick
			label="Root disk class"
			value={storageClass}
			onpick={(c) => (storageClass = c)}
			classes={classRows}
		/>
		{#each disks.rows as d, i (i)}
			<div class="flex flex-col gap-2 rounded-md border border-line p-2.5">
				<div class="flex items-center gap-2">
					<input
						class="{FIELD} flex-1 font-mono"
						bind:value={d.name}
						aria-label="Disk {i + 2} name"
						aria-invalid={!!diskError(i)}
					/>
					<input
						class="{FIELD} w-24! font-mono"
						bind:value={d.size}
						aria-label="Disk {i + 2} size"
					/>
					<button
						type="button"
						onclick={() => disks.remove(i)}
						aria-label="Remove disk {d.name}"
						class="flex size-9 shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong hover:text-danger"
						><X size={16} /></button
					>
				</div>
				{#if diskError(i)}<p class="text-xs text-danger-ink">{diskError(i)}</p>{/if}
				<ClassPick
					label="{d.name} class"
					value={d.storageClass}
					onpick={(c) => (d.storageClass = c)}
					classes={classRows}
				/>
			</div>
		{/each}
		<div><Button variant="link" onclick={disks.add}>Add a disk</Button></div>
	</div>
{/snippet}

{#snippet accessEditor(done: () => void)}
	<div class="flex flex-col gap-3">
		<div class="flex items-end gap-2">
			<div class="flex min-w-0 flex-1 flex-col gap-1.5">
				<label class={LABEL} for="vm-user">Guest user</label>
				<input id="vm-user" class="{FIELD} font-mono" bind:value={user} placeholder="cloud-user" />
			</div>
			<button
				type="button"
				onclick={done}
				class="h-9 rounded-md border border-line bg-panel px-3 text-sm text-ink hover:border-ink-faint"
				>Done</button
			>
		</div>
		<div class="flex flex-col gap-1.5">
			<label class={LABEL} for="vm-key">SSH public key</label>
			<textarea
				id="vm-key"
				rows="2"
				class="{FIELD} h-auto! py-2 font-mono text-xs"
				bind:value={sshKey}
				placeholder="ssh-ed25519 AAAA..."></textarea>
			<span class={HELP}>With a key, the password works on the console only.</span>
		</div>
		<div class="flex flex-col gap-1.5">
			<label class={LABEL} for="vm-pw">Console password</label>
			<div class="flex gap-2">
				<input
					id="vm-pw"
					class="{FIELD} flex-1 font-mono"
					bind:value={password}
					autocomplete="off"
				/>
				<button
					type="button"
					onclick={() => (password = generatePassword())}
					class="h-9 rounded-md border border-line bg-panel px-3 text-[13px] text-ink hover:border-ink-faint"
					>Regenerate</button
				>
			</div>
			<span class={HELP}
				>{#if password}<CircleCheck size={13} />Shown once after staging. Git keeps only a hash.{:else}Empty:
					no password login.{/if}</span
			>
		</div>
	</div>
{/snippet}
