<script lang="ts">
	import { untrack } from 'svelte';
	import { Check, Upload } from '@lucide/svelte';
	import { api, Unauthorized, type StorageClassInfo } from '#lib/api.js';
	import { liveMigrates } from '#lib/create.js';
	import { friendlyError } from '#lib/format.js';
	import { resource } from '#lib/resource.svelte.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Button from '../Button.svelte';
	import Consequence from '../Consequence.svelte';
	import ClassPick from './ClassPick.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, plural, useSheet } from './sheet.svelte.js';

	// A boot image: dotvirt creates the upload target (and, for a boot image,
	// registers it so New VM offers it) and mints a token, then the browser
	// streams the file straight to the cluster's upload proxy, so multi-GB
	// images never pass through dotvirt. It runs now: an image is a disk, not
	// config, so no pull request carries it.
	let {
		namespace = $bindable(),
		file: dropped,
		bootImage,
		onclose,
	}: {
		namespace: string;
		file?: File; // dropped where the sheet was opened
		bootImage?: boolean; // unset = a boot image, the usual reason to upload
		onclose: () => void;
	} = $props();

	const sheet = useSheet();
	inventory.loadOptions();
	// svelte-ignore state_referenced_locally
	const opener = inventory.projectOf(namespace);
	const places = $derived(inventory.namespaces);
	$effect(() => {
		const list = places;
		untrack(() => {
			if (!list.includes(namespace)) namespace = list[0] ?? '';
		});
	});

	type Phase = 'form' | 'creating' | 'preparing' | 'uploading' | 'importing' | 'done' | 'error';
	let phase = $state<Phase>('form');
	let error = $state('');
	let pct = $state(0);
	let importInfo = $state('');

	let file = $state<File | null>(null);
	// What the caller may do in the picked namespace, asked when it changes. A
	// missing answer or field reads no: Upload waits for a yes, and the
	// boot-image choice shows only where the route would take it.
	const canImage = $derived(!!(namespace && caps.ns(namespace)?.bootImageCreate));
	const upload = $derived(
		caps.nsGate(namespace, (n) => n.dataVolumeCreate, `You may not upload images in ${namespace}.`),
	);
	// svelte-ignore state_referenced_locally
	let offerImage = $state(bootImage ?? true);
	const asBoot = $derived(offerImage && canImage);
	let image = $state(''); // the boot image registered, "namespace/name"
	let name = $state('');
	let size = $state('10Gi');
	let storageClass = $state('');
	let dragging = $state(false);

	const classesRes = resource<StorageClassInfo[]>(() => 'storage-classes', api.storageClasses);
	const classRows = $derived(
		classesRes.data?.length ? classesRes.data : (inventory.options?.storageClasses ?? []),
	);
	const info = $derived(
		classesRes.data?.find((c) => c.name === (storageClass || inventory.defaultStorageClass)),
	);

	const sizeOK = $derived(/^\d+(Mi|Gi|Ti)$/.test(size));
	const bootTaken = $derived(
		(inventory.options?.osImages ?? []).some((i) => i.namespace === namespace && i.name === name),
	);
	const missing = $derived.by(() => {
		const m: string[] = [];
		// Rights first: no file is worth picking where the upload is refused.
		if (namespace && !upload.ok) m.push(upload.why);
		if (!file) m.push('Pick an image file');
		if (!name) m.push('Name the disk');
		else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
		if (!namespace) m.push('Pick a project and namespace');
		if (!sizeOK) m.push('The size needs a unit, like 10Gi');
		if (asBoot && bootTaken) m.push(`${name} is already a boot image in ${namespace}`);
		return m;
	});

	function take(f: File | null) {
		file = f;
		if (!f) return;
		if (!name)
			name = f.name
				.replace(/\.[^.]+$/, '')
				.toLowerCase()
				.replace(/[^a-z0-9-]/g, '-')
				.replace(/^-+|-+$/g, '');
		// A little larger than the file: a qcow2's virtual size can exceed it.
		size = `${Math.max(1, Math.ceil(f.size / 1024 ** 3) + 1)}Gi`;
	}
	// svelte-ignore state_referenced_locally
	if (dropped) take(dropped);

	const ORDER: Phase[] = ['creating', 'preparing', 'uploading', 'importing', 'done'];
	const at = $derived(ORDER.indexOf(phase));
	const running = $derived(at >= 0 && phase !== 'done');
	$effect(() => {
		sheet.pinned = running;
	});

	// Torn down (sign-out, navigation): stop the stream and the polls.
	const teardown = new AbortController();
	$effect(() => () => teardown.abort());
	const sleep = (ms: number) =>
		new Promise<void>((resolve, reject) => {
			const abort = () => {
				clearTimeout(id);
				reject(teardown.signal.reason);
			};
			const id = setTimeout(() => {
				teardown.signal.removeEventListener('abort', abort);
				resolve();
			}, ms);
			teardown.signal.addEventListener('abort', abort, { once: true });
		});

	async function until(pred: (phase: string) => boolean, label: string): Promise<void> {
		for (let i = 0; i < 600; i++) {
			teardown.signal.throwIfAborted();
			const st = await api.uploadStatus(namespace, name);
			if (st.progress) importInfo = st.progress;
			if (st.phase === 'Failed') throw new Error('The cluster reported the upload failed');
			if (pred(st.phase)) return;
			await sleep(2000);
		}
		throw new Error(`Timed out waiting for ${label}`);
	}

	// Straight to the upload proxy; XHR reports upload progress, fetch cannot.
	function stream(url: string, token: string): Promise<void> {
		return new Promise((resolve, reject) => {
			teardown.signal.throwIfAborted();
			const xhr = new XMLHttpRequest();
			teardown.signal.addEventListener(
				'abort',
				() => {
					xhr.abort();
					reject(teardown.signal.reason);
				},
				{ once: true },
			);
			xhr.open('POST', url);
			xhr.setRequestHeader('Authorization', `Bearer ${token}`);
			xhr.upload.onprogress = (e) => {
				if (e.lengthComputable) pct = Math.round((e.loaded / e.total) * 100);
			};
			xhr.onload = () =>
				xhr.status >= 200 && xhr.status < 300
					? resolve()
					: reject(new Error(`proxy ${xhr.status}: ${xhr.responseText || xhr.statusText}`));
			xhr.onerror = () =>
				reject(
					new Error(
						'The upload failed. The browser may not trust the upload proxy certificate: open it once to accept it.',
					),
				);
			xhr.send(file);
		});
	}

	async function start() {
		if (missing.length || running) return;
		error = '';
		try {
			phase = 'creating';
			const target = await api.createUpload({
				namespace,
				name,
				size,
				storageClass: storageClass || undefined,
				bootImage: asBoot,
			});
			image = target.image ?? '';
			phase = 'preparing';
			await until((p) => p === 'UploadReady', 'storage to be ready');
			const { token, uploadUrl } = await api.uploadToken(namespace, name);
			phase = 'uploading';
			await stream(uploadUrl, token);
			phase = 'importing';
			await until((p) => p === 'Succeeded', 'the import to finish');
			phase = 'done';
			// The catalog New VM reads gains the image now, not at the next sign-in.
			if (image) void inventory.loadOptions(true);
		} catch (e) {
			if (e instanceof Unauthorized || teardown.signal.aborted) return;
			error = friendlyError(e);
			phase = 'error';
		}
	}
	sheet.submit = start;

	const STEPS: { at: number; label: () => string }[] = [
		{ at: 0, label: () => 'Creating the disk' },
		{ at: 1, label: () => 'Preparing storage' },
		{ at: 2, label: () => `Uploading${phase === 'uploading' ? `, ${pct}%` : ''}` },
		{
			at: 3,
			label: () => `Importing${phase === 'importing' && importInfo ? `, ${importInfo}` : ''}`,
		},
	];
</script>

<SheetBody {error}>
	{#snippet form()}
		{#if phase === 'form' || phase === 'error'}
			<label
				class="flex cursor-pointer flex-col items-center gap-1.5 rounded-lg border border-dashed px-4 py-6 text-center {dragging
					? 'border-accent bg-select-soft'
					: 'border-line-strong bg-inset/60 hover:border-accent'}"
				ondragover={(e) => {
					e.preventDefault();
					dragging = true;
				}}
				ondragleave={() => (dragging = false)}
				ondrop={(e) => {
					e.preventDefault();
					dragging = false;
					take(e.dataTransfer?.files?.[0] ?? null);
				}}
			>
				<Upload size={20} class="text-accent-ink" />
				{#if file}
					<span class="font-mono text-sm font-medium text-ink">{file.name}</span>
					<span class="text-xs text-ink-muted"
						>{(file.size / 1024 ** 2).toFixed(1)} MiB. Drop another to replace it.</span
					>
				{:else}
					<span class="text-sm font-medium text-ink">Drop a disk image here, or pick a file</span>
					<span class="text-xs text-ink-muted">qcow2, raw, iso, or compressed</span>
				{/if}
				<input
					type="file"
					class="sr-only"
					accept=".qcow2,.img,.raw,.iso,.gz,.xz"
					onchange={(e) => take(e.currentTarget.files?.[0] ?? null)}
					data-autofocus
				/>
			</label>

			<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="img-name">Name</label>
					<input
						id="img-name"
						class="{FIELD} font-mono font-medium"
						bind:value={name}
						placeholder="fedora-42"
						autocomplete="off"
						spellcheck="false"
					/>
					{#if name && !validName(name)}<span class="{HELP} text-warn-ink!">{NAME_HINT}</span>{/if}
				</div>
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="img-where">Project and namespace</label>
					<PlacePicker id="img-where" bind:value={namespace} namespaces={places} first={opener} />
					<span class={HELP}
						>Where the disk lives. Search {plural(
							new Set(places.map((n) => inventory.projectOf(n))).size,
							'project',
						)}.</span
					>
				</div>
			</div>
			<div class="flex flex-col gap-1.5">
				<label class={LABEL} for="img-size">Disk size</label>
				<input id="img-size" class="{FIELD} w-32! font-mono" bind:value={size} />
				<span class={HELP}>At least the image's virtual size.</span>
			</div>
			<ClassPick
				label="Storage class"
				value={storageClass}
				onpick={(c) => (storageClass = c)}
				classes={classRows}
			/>
			{#if canImage}
				<label class="flex cursor-pointer items-center gap-2 text-sm text-ink">
					<input type="checkbox" class="size-4 accent-accent" bind:checked={offerImage} />
					Offer it as a boot image, so new VMs can start from it
				</label>
			{/if}
		{:else}
			<ol class="flex flex-col gap-3 py-1">
				{#each STEPS as s (s.at)}
					{@const complete = at > s.at}
					{@const active = at === s.at}
					<li class="flex items-center gap-2.5 text-sm">
						<span
							class="flex size-5 items-center justify-center rounded-full text-[11px] {complete
								? 'bg-ok text-white'
								: active
									? 'bg-accent text-white'
									: 'bg-inset-strong text-ink-faint'}"
							>{#if complete}<Check size={12} />{:else}{s.at + 1}{/if}</span
						>
						<span class={active || complete ? 'text-ink' : 'text-ink-faint'}>{s.label()}</span>
					</li>
					{#if active && phase === 'uploading'}
						<div class="ml-7 h-2 overflow-hidden rounded-full bg-inset-strong">
							<div class="h-full rounded-full bg-accent" style:width="{pct}%"></div>
						</div>
					{/if}
				{/each}
			</ol>
			{#if phase === 'done'}
				<p class="rounded-lg border border-ok/40 bg-ok-soft px-3.5 py-2.5 text-[13px] text-ok-ink">
					<span class="font-mono font-medium">{name}</span>
					{image ? 'is a boot image in' : 'is ready in'}
					<span class="font-mono">{namespace}</span>.
				</p>
			{/if}
		{/if}
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path=""
			project={inventory.projectOf(namespace)}
			mode="now"
			merges="Uploads now, straight from your browser to the cluster. No pull request: an image is a disk, not config."
		>
			{#snippet consequences()}
				<Consequence tone="info"
					>A disk named {name || 'after the file'} in {namespace || 'that namespace'}, ready once
					imported.</Consequence
				>
				{#if asBoot}
					<Consequence tone="ok">New VM lists it as a boot image.</Consequence>
				{/if}
				{#if info}
					<Consequence tone={liveMigrates(info) ? 'ok' : 'warn'}
						>{liveMigrates(info) ? 'Any host can read it.' : 'It stays on one host.'}</Consequence
					>
				{/if}
				<Consequence>Keep this tab open until it finishes.</Consequence>
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		{#if phase === 'done'}
			<span class="flex-1 text-ink-muted">Uploaded and imported.</span>
			{#if image}
				<Button variant="secondary" class="h-9! px-3!" onclick={onclose}>Done</Button>
				<Button
					class="h-9! px-4!"
					onclick={() => (ui.modal = { kind: 'newVM', namespaces: null, namespace, image })}
					>New VM from it</Button
				>
			{:else}
				<Button class="h-9! px-4!" onclick={onclose}>Done</Button>
			{/if}
		{:else if running}
			<span class="flex-1 text-ink-muted">Working. Keep this tab open.</span>
		{:else}
			<SheetFooter
				label={phase === 'error' ? 'Retry upload' : 'Upload image'}
				{missing}
				onsubmit={start}
				oncancel={onclose}
				note="Starts as soon as you upload."
			/>
		{/if}
	{/snippet}
</SheetBody>
