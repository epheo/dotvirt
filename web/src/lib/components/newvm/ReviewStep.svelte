<script lang="ts">
	import type { Network, Options } from '#lib/api.js';
	import { kindLabel, attachRef } from '#lib/networks.js';
	import Button from '../Button.svelte';

	let {
		name,
		namespace,
		osImage,
		preference,
		instancetype,
		running,
		diskSize,
		storageClass,
		extraDisks,
		attachPrimary,
		primaryLabel,
		selectedNetworks,
		hasNetwork,
		user,
		password,
		sshKey,
		networks,
		options,
		onedit,
	}: {
		name: string;
		namespace: string;
		osImage: string;
		preference: string;
		instancetype: string;
		running: boolean;
		diskSize: string;
		storageClass: string;
		extraDisks: { name: string; size: string; storageClass: string }[];
		attachPrimary: boolean;
		primaryLabel: string;
		selectedNetworks: string[];
		hasNetwork: boolean;
		user: string;
		password: string;
		sshKey: string;
		networks: Network[];
		options: Options | null;
		onedit: (step: number) => void;
	} = $props();

	// Display labels: names, not raw refs/ids.
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
	// Unmet required fields, with the step to jump to, so a disabled Finish is
	// always self-explanatory.
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
</script>

{#snippet reviewGroup(title: string, step: number, rows: string[][])}
	<div class="rounded border border-line">
		<div class="flex items-center justify-between border-b border-line-soft bg-inset px-3 py-1.5">
			<span class="text-xs font-semibold tracking-wide text-ink-muted uppercase">{title}</span>
			<Button variant="link" size="sm" onclick={() => onedit(step)}>Edit</Button>
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

<div class="space-y-3">
	<p class="text-xs text-ink-muted">Review your selections, then stage the VM.</p>
	{#if missing.length}
		<div class="rounded border border-warn-soft bg-warn-soft/60 p-3 text-xs text-warn-ink">
			<p class="mb-1 font-medium">Complete the required fields to stage this VM:</p>
			<ul class="space-y-0.5">
				{#each missing as m (m.label)}
					<li class="flex items-center justify-between gap-2">
						<span>• {m.label}</span>
						<Button variant="link" onclick={() => onedit(m.step)}>Edit</Button>
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
		['cloud-init user', user || 'image default'],
		['Console password', password ? 'generated, shown once after staging' : 'disabled'],
		['SSH key', sshKey ? 'provided' : 'None'],
	])}
</div>
