<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, type CreateVMRequest, type Network, type Options } from '$lib/api';
	import { kindLabel, attachableNetworks, attachRef } from '$lib/networks';
	import Modal from './Modal.svelte';
	import Wizard from './Wizard.svelte';

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
	let extraDisks = $state<{ name: string; size: string }[]>([]);
	// Selected secondary networks, held as attach refs ("namespace/nad", or a bare
	// name for a shared CUDN). The project's default "VM Network" is implicit.
	let selectedNetworks = $state<string[]>([]);

	let submitting = $state(false);
	let error = $state('');

	// The active wizard step (bound into <Wizard>); the review step's Edit links
	// seek it back to a specific step.
	let current = $state(0);

	// Attachable secondary port groups for the chosen project: shared (CUDN)
	// networks plus this namespace's own non-default networks. The primary
	// ("VM Network") is excluded — it backs the default NIC automatically.
	const available = $derived(attachableNetworks(networks, namespace));
	function toggleNet(n: Network, on: boolean) {
		const ref = attachRef(n);
		selectedNetworks = on ? [...selectedNetworks, ref] : selectedNetworks.filter((r) => r !== ref);
	}
	// Project networks are namespace-bound, so clear the selection when the target
	// project changes (a stale pick from another project mustn't carry over).
	$effect(() => {
		namespace;
		selectedNetworks = [];
	});

	$effect(() => {
		if (!namespace) namespace = namespaces[0] ?? 'default';
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

	// Global gate (unchanged): reused as both the Finish gate and submit()'s guard.
	const valid = $derived(!!(name && namespace && osImage && instancetype && preference));
	// Per-step validity — drives only the rail markers, never blocks navigation.
	const step1Valid = $derived(!!(name && namespace));
	const step2Valid = $derived(!!(osImage && preference));
	const step3Valid = $derived(!!instancetype);

	function addDisk() {
		extraDisks = [...extraDisks, { name: `disk${extraDisks.length + 1}`, size: '10Gi' }];
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
			for (const d of extraDisks) rows.push([`Extra disk · ${d.name}`, d.size]);
		else rows.push(['Extra disks', 'None']);
		return rows;
	});
	const networkRows = $derived.by(() => {
		const rows: string[][] = [['Default network', 'attached automatically']];
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
			extraDisks: extraDisks.length ? extraDisks : undefined,
			networks: selectedNetworks.length ? selectedNetworks.map((n) => ({ name: n })) : undefined,
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
		<p class="text-xs text-slate-500">
			Name the virtual machine and choose the project it belongs to.
		</p>
		<label class="block">
			<span class="text-slate-600">Name</span>
			<input
				bind:value={name}
				placeholder="my-vm"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>
		<label class="block">
			<span class="text-slate-600">Project (namespace)</span>
			<select
				bind:value={namespace}
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			>
				{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
			</select>
		</label>
	</div>
{/snippet}

{#snippet step2()}
	<div class="space-y-4">
		<p class="text-xs text-slate-500">
			Select the OS image to boot from and a preference that tunes the VM for that guest.
		</p>
		<label class="block">
			<span class="text-slate-600">OS image</span>
			<select bind:value={osImage} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
				{#each (options?.osImages ?? []).filter((i) => i.ready) as img (img.namespace + img.name)}
					<option value={`${img.name}|${img.namespace}`}>{img.name}</option>
				{/each}
			</select>
		</label>
		<label class="block">
			<span class="text-slate-600">Preference (OS tuning)</span>
			<select
				bind:value={preference}
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			>
				{#each options?.preferences ?? [] as p (p.name)}
					<option value={p.name}>{p.displayName || p.name}</option>
				{/each}
			</select>
		</label>
	</div>
{/snippet}

{#snippet step3()}
	<div class="space-y-4">
		<p class="text-xs text-slate-500">
			Choose a size (instance type) and whether to power the VM on after creation.
		</p>
		<label class="block">
			<span class="text-slate-600">Size (instancetype)</span>
			<select
				bind:value={instancetype}
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			>
				{#each options?.instancetypes ?? [] as it (it.name)}
					<option value={it.name}>{it.name} — {it.cpu} CPU / {it.memory}</option>
				{/each}
			</select>
		</label>
		<label class="flex items-center gap-2">
			<input type="checkbox" bind:checked={running} />
			<span class="text-slate-600">Start immediately (runStrategy: Always)</span>
		</label>
	</div>
{/snippet}

{#snippet step4()}
	<div class="space-y-4">
		<p class="text-xs text-slate-500">Configure the boot disk and any additional disks.</p>
		<div class="grid grid-cols-2 gap-4">
			<label class="block">
				<span class="text-slate-600">Root disk size</span>
				<input
					bind:value={diskSize}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
			</label>
			<label class="block">
				<span class="text-slate-600">Storage class</span>
				<select
					bind:value={storageClass}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				>
					<option value="">cluster default</option>
					{#each options?.storageClasses ?? [] as sc (sc.name)}
						<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
					{/each}
				</select>
			</label>
		</div>
		<div>
			<div class="mb-1 flex items-center justify-between">
				<span class="text-slate-600">Extra disks</span>
				<button onclick={addDisk} type="button" class="text-xs text-blue-600 hover:underline"
					>+ Add disk</button
				>
			</div>
			{#each extraDisks as disk, i (i)}
				<div class="mb-1 flex gap-2">
					<input
						bind:value={disk.name}
						placeholder="name"
						class="w-1/2 rounded border border-slate-300 px-2 py-1"
					/>
					<input
						bind:value={disk.size}
						placeholder="10Gi"
						class="w-1/3 rounded border border-slate-300 px-2 py-1"
					/>
					<button
						onclick={() => removeDisk(i)}
						type="button"
						aria-label="Remove disk"
						class="text-red-500 hover:text-red-700"><X size={14} /></button
					>
				</div>
			{/each}
		</div>
	</div>
{/snippet}

{#snippet step5()}
	<div class="space-y-2">
		<p class="text-xs text-slate-500">
			The project's default network is attached automatically. Add extra network adapters below.
		</p>
		{#if available.length}
			<div class="mt-1 space-y-1 rounded border border-slate-300 p-2">
				{#each available as net (net.scope + '/' + (net.namespace ?? '') + '/' + net.name)}
					<label class="flex items-center gap-2 text-xs">
						<input
							type="checkbox"
							checked={selectedNetworks.includes(attachRef(net))}
							onchange={(e) => toggleNet(net, e.currentTarget.checked)}
						/>
						<span class="text-slate-700">{net.name}</span>
						<span class="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500"
							>{kindLabel(net.kind)}{net.vlan ? ` ${net.vlan}` : ''}</span
						>
						{#if net.scope === 'shared'}<span class="text-[11px] text-slate-400">shared</span>{/if}
					</label>
				{/each}
			</div>
		{:else}
			<p class="mt-1 text-xs text-slate-400">No additional networks available for {namespace}.</p>
		{/if}
	</div>
{/snippet}

{#snippet step6()}
	<div class="space-y-4">
		<p class="text-xs text-slate-500">
			Optional cloud-init: a default user and an SSH key injected at first boot.
		</p>
		<label class="block">
			<span class="text-slate-600"
				>cloud-init user <span class="text-slate-400">(optional)</span></span
			>
			<input
				bind:value={user}
				placeholder="fedora"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>
		<label class="block">
			<span class="text-slate-600"
				>SSH public key <span class="text-slate-400">(optional)</span></span
			>
			<input
				bind:value={sshKey}
				placeholder="ssh-ed25519 AAAA…"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>
	</div>
{/snippet}

{#snippet reviewGroup(title: string, step: number, rows: string[][])}
	<div class="rounded border border-slate-200">
		<div
			class="flex items-center justify-between border-b border-slate-100 bg-slate-50 px-3 py-1.5"
		>
			<span class="text-xs font-semibold tracking-wide text-slate-500 uppercase">{title}</span>
			<button
				type="button"
				onclick={() => (current = step)}
				class="text-xs text-blue-600 hover:underline">Edit</button
			>
		</div>
		<dl class="divide-y divide-slate-100">
			{#each rows as r (r[0])}
				<div class="flex justify-between gap-3 px-3 py-1.5">
					<dt class="text-slate-500">{r[0]}</dt>
					<dd class="text-right text-slate-800">{r[1]}</dd>
				</div>
			{/each}
		</dl>
	</div>
{/snippet}

{#snippet review()}
	<div class="space-y-3">
		<p class="text-xs text-slate-500">Review your selections, then stage the VM.</p>
		{#if missing.length}
			<div class="rounded border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
				<p class="mb-1 font-medium">Complete the required fields to stage this VM:</p>
				<ul class="space-y-0.5">
					{#each missing as m (m.label)}
						<li class="flex items-center justify-between gap-2">
							<span>• {m.label}</span>
							<button
								type="button"
								onclick={() => (current = m.step)}
								class="text-blue-700 hover:underline">Edit</button
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
			<p class="rounded bg-red-50 px-3 py-2 text-sm text-red-700">
				Failed to load options: {loadError}
			</p>
		</div>
	</Modal>
{:else if !options}
	<Modal title="New Virtual Machine" {onclose}>
		<div class="px-5 py-4">
			<p class="text-sm text-slate-400">Loading cluster options…</p>
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
