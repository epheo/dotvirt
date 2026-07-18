<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, type CreateVMRequest, type Network, type Options } from '$lib/api';
	import { kindLabel, attachableNetworks, attachRef } from '$lib/networks';
	import Modal from './Modal.svelte';
	import Wizard from './Wizard.svelte';
	import NamespaceSelect from './NamespaceSelect.svelte';
	import FormField from './FormField.svelte';

	let {
		namespaces,
		networks = [],
		onclose,
		onstaged,
	}: {
		namespaces: string[];
		networks?: Network[]; // port-group catalog (from the page), for the adapter picker
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let options = $state<Options | null>(null);
	let loadError = $state('');

	// Form state
	let name = $state('');
	// Default to the first project; seeded in the mount effect to avoid capturing
	// only the initial prop value.
	let namespace = $state('');
	let osImage = $state(''); // "name|namespace"
	let instancetype = $state('');
	let preference = $state('');
	let diskSize = $state('30Gi');
	let storageClass = $state(''); // '' = cluster default
	let running = $state(true);
	let sshKey = $state('');
	let user = $state('');
	let extraDisks = $state<{ name: string; size: string; storageClass: string }[]>([]);
	// Selected secondary networks, held as attach refs ("namespace/nad", or a bare
	// name for a shared CUDN).
	let selectedNetworks = $state<string[]>([]);
	// Attach the primary (pod-network) NIC. On by default, but — unlike a pod — a VM
	// may decline it and run on secondary networks alone.
	let attachPrimary = $state(true);

	let submitting = $state(false);
	let error = $state('');

	// The active wizard step (bound into <Wizard>); the review step's Edit links
	// seek it back to a specific step.
	let current = $state(0);

	// Attachable secondary port groups for the chosen project: shared (CUDN)
	// networks plus this namespace's own non-default networks. The primary
	// ("VM Network") is excluded here — it's offered as its own toggle below.
	const available = $derived(attachableNetworks(networks, namespace));
	// The project's primary ("VM Network") UDN, when one exists — else the primary
	// NIC is the cluster default pod network, shown with a generic label.
	const primaryNet = $derived(
		networks.find((n) => n.kind === 'default' && n.namespace === namespace),
	);
	const primaryLabel = $derived(primaryNet?.name ?? 'Pod network');
	function toggleNet(n: Network, on: boolean) {
		const ref = attachRef(n);
		selectedNetworks = on ? [...selectedNetworks, ref] : selectedNetworks.filter((r) => r !== ref);
	}
	// Project networks are namespace-bound, so clear the selection when the target
	// project changes (a stale pick from another project mustn't carry over); the
	// primary NIC resets to its attached default.
	$effect(() => {
		namespace;
		selectedNetworks = [];
		attachPrimary = true;
	});

	$effect(() => {
		api
			.options()
			.then((o) => {
				options = o;
				// Sensible defaults from what's available. Guard each list — a source
				// the backend SA can't read comes back empty (or null on an old build).
				const osImages = o.osImages ?? [];
				const fed =
					osImages.find((i) => i.ready && i.name === 'fedora') ?? osImages.find((i) => i.ready);
				if (fed) osImage = `${fed.name}|${fed.namespace}`;
				preference =
					(o.preferences ?? []).find((p) => p.name === 'fedora')?.name ??
					o.preferences?.[0]?.name ??
					'';
				instancetype =
					(o.instancetypes ?? []).find((i) => i.name === 'u1.medium')?.name ??
					o.instancetypes?.[0]?.name ??
					'';
			})
			.catch((e) => (loadError = String(e)));
	});

	// A VM needs at least one NIC: the primary, a secondary, or both.
	const hasNetwork = $derived(attachPrimary || selectedNetworks.length > 0);
	// Global gate: reused as both the Finish gate and submit()'s guard.
	const valid = $derived(
		!!(name && namespace && osImage && instancetype && preference) && hasNetwork,
	);
	// Per-step validity — drives only the rail markers, never blocks navigation.
	const step1Valid = $derived(!!(name && namespace));
	const step2Valid = $derived(!!(osImage && preference));
	const step3Valid = $derived(!!instancetype);

	function addDisk() {
		extraDisks = [
			...extraDisks,
			{ name: `disk${extraDisks.length + 1}`, size: '10Gi', storageClass: '' },
		];
	}
	function removeDisk(i: number) {
		extraDisks = extraDisks.filter((_, idx) => idx !== i);
	}

	// Review-step display labels (names, not raw refs/ids).
	const osImageName = $derived(osImage ? osImage.split('|')[0] : '');
	const preferenceLabel = $derived(
		(options?.preferences ?? []).find((p) => p.name === preference)?.displayName || preference,
	);
	const instancetypeLabel = $derived.by(() => {
		const it = (options?.instancetypes ?? []).find((i) => i.name === instancetype);
		return it ? `${it.name} — ${it.cpu} CPU / ${it.memory}` : instancetype;
	});
	const selectedNetworkLabels = $derived(
		selectedNetworks.map((ref) => {
			const n = networks.find((x) => attachRef(x) === ref);
			return n ? `${n.name} (${kindLabel(n.kind)})` : ref;
		}),
	);
	const storageRows = $derived.by(() => {
		const rows: string[][] = [
			['Root disk', diskSize || '—'],
			['Storage class', storageClass || 'cluster default'],
		];
		if (extraDisks.length)
			for (const d of extraDisks)
				rows.push([`Extra disk · ${d.name}`, `${d.size} · ${d.storageClass || 'cluster default'}`]);
		else rows.push(['Extra disks', 'None']);
		return rows;
	});
	const networkRows = $derived.by(() => {
		const rows: string[][] = [
			['Primary network', attachPrimary ? `${primaryLabel} (VM Network)` : 'Not attached'],
		];
		if (selectedNetworkLabels.length)
			for (const l of selectedNetworkLabels) rows.push(['Adapter', l]);
		else rows.push(['Additional adapters', 'None']);
		return rows;
	});
	// Unmet required fields, with the step to jump to — surfaced on the review step
	// so a disabled Finish is always self-explanatory.
	const missing = $derived.by(() => {
		const m: { label: string; step: number }[] = [];
		if (!name) m.push({ label: 'Name is required', step: 0 });
		if (!namespace) m.push({ label: 'Project is required', step: 0 });
		if (!osImage) m.push({ label: 'OS image is required', step: 1 });
		if (!preference) m.push({ label: 'Preference is required', step: 1 });
		if (!instancetype) m.push({ label: 'Size is required', step: 2 });
		if (!hasNetwork) m.push({ label: 'At least one network is required', step: 4 });
		return m;
	});

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
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
			extraDisks: extraDisks.length
				? extraDisks.map((d) => ({
						name: d.name,
						size: d.size,
						storageClass: d.storageClass || undefined,
					}))
				: undefined,
			networks: selectedNetworks.length ? selectedNetworks.map((n) => ({ name: n })) : undefined,
			primaryNetwork: attachPrimary,
		};
		if (user || sshKey) req.cloudInit = { user: user || undefined, sshKey: sshKey || undefined };
		try {
			await api.stageCreate(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<!-- Wizard step bodies. Only the active one renders, and only once options have
     loaded (the <Wizard> below is gated on `options`), so `options?.` guards keep
     the templates type-clean without re-narrowing inside each snippet. -->
{#snippet step1()}
	<div class="space-y-4">
		<p class="text-xs text-ink-muted">
			Name the virtual machine and choose the project it belongs to.
		</p>
		<FormField label="Name">
			<input
				bind:value={name}
				placeholder="my-vm"
				class="w-full rounded border border-line-strong px-2 py-1.5"
			/>
		</FormField>
		<NamespaceSelect bind:namespace {namespaces} fallback="default" />
	</div>
{/snippet}

{#snippet step2()}
	<div class="space-y-4">
		<p class="text-xs text-ink-muted">
			Select the OS image to boot from and a preference that tunes the VM for that guest.
		</p>
		<FormField label="OS image">
			<select bind:value={osImage} class="w-full rounded border border-line-strong px-2 py-1.5">
				{#each (options?.osImages ?? []).filter((i) => i.ready) as img (img.namespace + img.name)}
					<option value={`${img.name}|${img.namespace}`}>{img.name}</option>
				{/each}
			</select>
		</FormField>
		<FormField label="Preference (OS tuning)">
			<select bind:value={preference} class="w-full rounded border border-line-strong px-2 py-1.5">
				{#each options?.preferences ?? [] as p (p.name)}
					<option value={p.name}>{p.displayName || p.name}</option>
				{/each}
			</select>
		</FormField>
	</div>
{/snippet}

{#snippet step3()}
	<div class="space-y-4">
		<p class="text-xs text-ink-muted">
			Choose a size (instance type) and whether to power the VM on after creation.
		</p>
		<FormField label="Size (instancetype)">
			<select
				bind:value={instancetype}
				class="w-full rounded border border-line-strong px-2 py-1.5"
			>
				{#each options?.instancetypes ?? [] as it (it.name)}
					<option value={it.name}>{it.name} — {it.cpu} CPU / {it.memory}</option>
				{/each}
			</select>
		</FormField>
		<label class="flex items-center gap-2">
			<input type="checkbox" bind:checked={running} />
			<span class="text-ink-soft">Start immediately (runStrategy: Always)</span>
		</label>
	</div>
{/snippet}

{#snippet step4()}
	<div class="space-y-4">
		<p class="text-xs text-ink-muted">Configure the boot disk and any additional disks.</p>
		<div class="grid grid-cols-2 gap-4">
			<FormField label="Root disk size">
				<input bind:value={diskSize} class="w-full rounded border border-line-strong px-2 py-1.5" />
			</FormField>
			<FormField label="Storage class">
				<select
					bind:value={storageClass}
					class="w-full rounded border border-line-strong px-2 py-1.5"
				>
					<option value="">cluster default</option>
					{#each options?.storageClasses ?? [] as sc (sc.name)}
						<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
					{/each}
				</select>
			</FormField>
		</div>
		<div>
			<div class="mb-1 flex items-center justify-between">
				<span class="text-ink-soft">Extra disks</span>
				<button onclick={addDisk} type="button" class="text-xs text-accent hover:underline"
					>+ Add disk</button
				>
			</div>
			{#each extraDisks as disk, i (i)}
				<div class="mb-1 flex gap-2">
					<input
						bind:value={disk.name}
						placeholder="name"
						class="min-w-0 flex-1 rounded border border-line-strong px-2 py-1"
					/>
					<input
						bind:value={disk.size}
						placeholder="10Gi"
						class="w-20 rounded border border-line-strong px-2 py-1"
					/>
					<select
						bind:value={disk.storageClass}
						class="min-w-0 flex-1 rounded border border-line-strong px-2 py-1"
					>
						<option value="">cluster default</option>
						{#each options?.storageClasses ?? [] as sc (sc.name)}
							<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
						{/each}
					</select>
					<button
						onclick={() => removeDisk(i)}
						type="button"
						aria-label="Remove disk"
						class="text-danger hover:text-danger-ink"><X size={14} /></button
					>
				</div>
			{/each}
		</div>
	</div>
{/snippet}

{#snippet step5()}
	<div class="space-y-2">
		<p class="text-xs text-ink-muted">
			Choose the networks this VM attaches to. The primary is attached by default, but a VM may run
			on secondary networks alone — at least one is required.
		</p>
		<div class="mt-1 space-y-1 rounded border border-line-strong p-2">
			<!-- Primary (pod-network) NIC — optional for a VM, unlike a pod. -->
			<label class="flex items-center gap-2 text-xs">
				<input type="checkbox" bind:checked={attachPrimary} />
				<span class="text-ink-soft">{primaryLabel}</span>
				<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
					>VM Network</span
				>
				<span class="text-[11px] text-ink-faint">primary</span>
			</label>
			{#each available as net (net.scope + '/' + (net.namespace ?? '') + '/' + net.name)}
				<label class="flex items-center gap-2 text-xs">
					<input
						type="checkbox"
						checked={selectedNetworks.includes(attachRef(net))}
						onchange={(e) => toggleNet(net, e.currentTarget.checked)}
					/>
					<span class="text-ink-soft">{net.name}</span>
					<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
						>{kindLabel(net.kind)}{net.vlan ? ` ${net.vlan}` : ''}</span
					>
					{#if net.scope === 'shared'}<span class="text-[11px] text-ink-faint">shared</span>{/if}
				</label>
			{/each}
		</div>
		{#if !available.length}
			<p class="mt-1 text-xs text-ink-faint">No secondary networks available for {namespace}.</p>
		{/if}
		{#if !hasNetwork}
			<p class="text-xs text-warn-ink">Select at least one network — a VM needs a NIC.</p>
		{/if}
	</div>
{/snippet}

{#snippet step6()}
	<div class="space-y-4">
		<p class="text-xs text-ink-muted">
			Optional cloud-init: a default user and an SSH key injected at first boot.
		</p>
		<label class="block">
			<span class="text-ink-soft"
				>cloud-init user <span class="text-ink-faint">(optional)</span></span
			>
			<input
				bind:value={user}
				placeholder="fedora"
				class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
			/>
		</label>
		<label class="block">
			<span class="text-ink-soft"
				>SSH public key <span class="text-ink-faint">(optional)</span></span
			>
			<input
				bind:value={sshKey}
				placeholder="ssh-ed25519 AAAA…"
				class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
			/>
		</label>
	</div>
{/snippet}

{#snippet reviewGroup(title: string, step: number, rows: string[][])}
	<div class="rounded border border-line">
		<div class="flex items-center justify-between border-b border-line-soft bg-inset px-3 py-1.5">
			<span class="text-xs font-semibold tracking-wide text-ink-muted uppercase">{title}</span>
			<button
				type="button"
				onclick={() => (current = step)}
				class="text-xs text-accent hover:underline">Edit</button
			>
		</div>
		<dl class="divide-y divide-line-soft">
			{#each rows as r (r[0])}
				<div class="flex justify-between gap-3 px-3 py-1.5">
					<dt class="text-ink-muted">{r[0]}</dt>
					<dd class="text-right text-ink">{r[1]}</dd>
				</div>
			{/each}
		</dl>
	</div>
{/snippet}

{#snippet review()}
	<div class="space-y-3">
		<p class="text-xs text-ink-muted">Review your selections, then stage the VM.</p>
		{#if missing.length}
			<div class="rounded border border-warn-soft bg-warn-soft/60 p-3 text-xs text-warn-ink">
				<p class="mb-1 font-medium">Complete the required fields to stage this VM:</p>
				<ul class="space-y-0.5">
					{#each missing as m (m.label)}
						<li class="flex items-center justify-between gap-2">
							<span>• {m.label}</span>
							<button
								type="button"
								onclick={() => (current = m.step)}
								class="text-accent-ink hover:underline">Edit</button
							>
						</li>
					{/each}
				</ul>
			</div>
		{/if}
		{@render reviewGroup('Name and project', 0, [
			['Name', name || '—'],
			['Project', namespace || '—'],
		])}
		{@render reviewGroup('Guest OS', 1, [
			['OS image', osImageName || '—'],
			['Preference', preferenceLabel || '—'],
		])}
		{@render reviewGroup('Compute', 2, [
			['Size', instancetypeLabel || '—'],
			['Power', running ? 'Start immediately' : 'Create powered off'],
		])}
		{@render reviewGroup('Storage', 3, storageRows)}
		{@render reviewGroup('Networks', 4, networkRows)}
		{@render reviewGroup('Customize', 5, [
			['cloud-init user', user || 'default'],
			['SSH key', sshKey ? 'provided' : 'None'],
		])}
	</div>
{/snippet}

{#if loadError}
	<Modal title="New Virtual Machine" {onclose}>
		<div class="px-5 py-4">
			<p class="rounded bg-danger-soft/60 px-3 py-2 text-sm text-danger-ink">
				Failed to load options: {loadError}
			</p>
		</div>
	</Modal>
{:else if !options}
	<Modal title="New Virtual Machine" {onclose}>
		<div class="px-5 py-4">
			<p class="text-sm text-ink-faint">Loading cluster options…</p>
		</div>
	</Modal>
{:else}
	<Wizard
		title="New Virtual Machine"
		footerHint="Staged into the changeset; open a PR from “Changes”."
		finishLabel={submitting ? 'Staging…' : 'Stage VM'}
		canFinish={valid}
		{submitting}
		{error}
		bind:current
		onfinish={submit}
		{onclose}
		steps={[
			{ title: 'Name and project', valid: step1Valid, body: step1 },
			{ title: 'Guest OS', valid: step2Valid, body: step2 },
			{ title: 'Compute', valid: step3Valid, body: step3 },
			{ title: 'Storage', body: step4 },
			{ title: 'Networks', body: step5 },
			{ title: 'Customize', body: step6 },
			{ title: 'Ready to complete', body: review },
		]}
	/>
{/if}
