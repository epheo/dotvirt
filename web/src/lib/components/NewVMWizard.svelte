<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, type CreateVMRequest, type Options } from '$lib/api';

	let {
		namespaces,
		onclose,
		onstaged
	}: {
		namespaces: string[];
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
	let selectedNetworks = $state<string[]>([]);

	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (!namespace) namespace = namespaces[0] ?? 'default';
		api
			.options()
			.then((o) => {
				options = o;
				// Sensible defaults from what's available. Guard each list — a source
				// the backend SA can't read comes back empty (or null on an old build).
				const osImages = o.osImages ?? [];
				const fed = osImages.find((i) => i.ready && i.name === 'fedora') ?? osImages.find((i) => i.ready);
				if (fed) osImage = `${fed.name}|${fed.namespace}`;
				preference =
					(o.preferences ?? []).find((p) => p.name === 'fedora')?.name ?? o.preferences?.[0]?.name ?? '';
				instancetype =
					(o.instancetypes ?? []).find((i) => i.name === 'u1.medium')?.name ??
					o.instancetypes?.[0]?.name ??
					'';
			})
			.catch((e) => (loadError = String(e)));
	});

	const valid = $derived(!!(name && namespace && osImage && instancetype && preference));

	function addDisk() {
		extraDisks = [...extraDisks, { name: `disk${extraDisks.length + 1}`, size: '10Gi' }];
	}
	function removeDisk(i: number) {
		extraDisks = extraDisks.filter((_, idx) => idx !== i);
	}

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
			networks: selectedNetworks.length ? selectedNetworks.map((n) => ({ name: n })) : undefined
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

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && onclose()}
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	role="presentation"
>
	<div class="flex max-h-[90vh] w-full max-w-2xl flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2 class="text-base font-semibold text-slate-800">New Virtual Machine</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
		</header>

		<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
			{#if loadError}
				<p class="rounded bg-red-50 px-3 py-2 text-sm text-red-700">Failed to load options: {loadError}</p>
			{:else if !options}
				<p class="text-sm text-slate-400">Loading cluster options…</p>
			{:else}
				<div class="grid grid-cols-2 gap-4 text-sm">
					<label class="block">
						<span class="text-slate-600">Name</span>
						<input bind:value={name} placeholder="my-vm" class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5" />
					</label>
					<label class="block">
						<span class="text-slate-600">Project (namespace)</span>
						<select bind:value={namespace} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
							{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
						</select>
					</label>

					<label class="block">
						<span class="text-slate-600">OS image</span>
						<select bind:value={osImage} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
							{#each (options.osImages ?? []).filter((i) => i.ready) as img (img.namespace + img.name)}
								<option value={`${img.name}|${img.namespace}`}>{img.name}</option>
							{/each}
						</select>
					</label>
					<label class="block">
						<span class="text-slate-600">Preference (OS tuning)</span>
						<select bind:value={preference} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
							{#each options.preferences as p (p.name)}
								<option value={p.name}>{p.displayName || p.name}</option>
							{/each}
						</select>
					</label>

					<label class="block">
						<span class="text-slate-600">Size (instancetype)</span>
						<select bind:value={instancetype} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
							{#each options.instancetypes as it (it.name)}
								<option value={it.name}>{it.name} — {it.cpu} CPU / {it.memory}</option>
							{/each}
						</select>
					</label>
					<label class="block">
						<span class="text-slate-600">Root disk size</span>
						<input bind:value={diskSize} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5" />
					</label>

					<label class="block">
						<span class="text-slate-600">Storage class</span>
						<select bind:value={storageClass} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
							<option value="">cluster default</option>
							{#each options.storageClasses as sc (sc.name)}
								<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
							{/each}
						</select>
					</label>

					<label class="col-span-2 flex items-center gap-2">
						<input type="checkbox" bind:checked={running} />
						<span class="text-slate-600">Start immediately (runStrategy: Always)</span>
					</label>

					<!-- cloud-init -->
					<label class="block">
						<span class="text-slate-600">cloud-init user <span class="text-slate-400">(optional)</span></span>
						<input bind:value={user} placeholder="fedora" class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5" />
					</label>
					<label class="block">
						<span class="text-slate-600">SSH public key <span class="text-slate-400">(optional)</span></span>
						<input bind:value={sshKey} placeholder="ssh-ed25519 AAAA…" class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5" />
					</label>

					<!-- extra disks -->
					<div class="col-span-2">
						<div class="mb-1 flex items-center justify-between">
							<span class="text-slate-600">Extra disks</span>
							<button onclick={addDisk} type="button" class="text-xs text-blue-600 hover:underline">+ Add disk</button>
						</div>
						{#each extraDisks as disk, i (i)}
							<div class="mb-1 flex gap-2">
								<input bind:value={disk.name} placeholder="name" class="w-1/2 rounded border border-slate-300 px-2 py-1" />
								<input bind:value={disk.size} placeholder="10Gi" class="w-1/3 rounded border border-slate-300 px-2 py-1" />
								<button onclick={() => removeDisk(i)} type="button" aria-label="Remove disk" class="text-red-500 hover:text-red-700"><X size={14} /></button>
							</div>
						{/each}
					</div>

					<!-- networks -->
					<div class="col-span-2">
						<span class="text-slate-600">Additional networks <span class="text-slate-400">(besides pod default)</span></span>
						<select multiple bind:value={selectedNetworks} class="mt-1 h-20 w-full rounded border border-slate-300 px-2 py-1 text-xs">
							{#each options.networks ?? [] as net (net.namespace + net.name)}
								<option value={`${net.namespace}/${net.name}`}>{net.namespace}/{net.name}</option>
							{/each}
						</select>
					</div>
				</div>

				{#if error}
					<pre class="mt-3 rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
				{/if}
			{/if}
		</div>

		<footer class="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
			<span class="text-xs text-slate-400">Staged into the changeset; open a PR from “Changes”.</span>
			<button onclick={onclose} class="ml-auto rounded px-4 py-1.5 text-sm text-slate-600 hover:bg-slate-100">Cancel</button>
			<button
				onclick={submit}
				disabled={!valid || submitting}
				class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
			>
				{submitting ? 'Staging…' : 'Stage VM'}
			</button>
		</footer>
	</div>
</div>
