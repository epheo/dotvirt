<script lang="ts">
	import { untrack } from 'svelte';
	import { Check, Upload } from '@lucide/svelte';
	import { api, Unauthorized, type StorageClassInfo } from '#lib/api.js';
	import { liveMigrates } from '#lib/create.js';
	import { friendlyError, plural } from '#lib/format.js';
	import { imageGate } from '#lib/gates.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui, type ImageDownload } from '#lib/state/ui.svelte.js';
	import { imageDeleteConsequence, suggestImageName } from '#lib/storage.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Button from '../Button.svelte';
	import Consequence from '../Consequence.svelte';
	import ErrorNote from '../ErrorNote.svelte';
	import StageChip from '../StageChip.svelte';
	import ClassPick from './ClassPick.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, useSheet } from './sheet.svelte.js';

	// A boot image from one of three sources. A file: dotvirt creates the
	// upload target (and, for a boot image, registers it so New VM offers it)
	// and mints a token, then the browser streams the file straight to the
	// cluster's upload proxy, so multi-GB images never pass through dotvirt.
	// A URL or a registry: dotvirt creates a DataVolume CDI fills by
	// downloading the source itself, and registers it; no upload proxy takes
	// part. Either runs now: an image is a disk, not config, so no pull
	// request carries it.
	let {
		namespace = $bindable(),
		file: dropped,
		bootImage,
		download: given,
		onclose,
	}: {
		namespace: string;
		file?: File; // dropped where the sheet was opened
		bootImage?: boolean; // unset = a boot image, the usual reason to upload
		download?: ImageDownload; // opened on a download, prefilled when it names a source
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

	type Source = 'file' | 'url' | 'registry';
	// svelte-ignore state_referenced_locally
	let source = $state<Source>(
		given?.kind ?? (!dropped && caps.cluster?.imageUpload === false ? 'url' : 'file'),
	);
	const downloads = $derived(source !== 'file');

	type Phase = 'form' | 'creating' | 'preparing' | 'uploading' | 'importing' | 'done' | 'error';
	let phase = $state<Phase>('form');
	let error = $state('');
	let pct = $state(0);
	let importInfo = $state('');
	let importError = $state(''); // the importer's last failure while the cluster retries it
	let still = $state(false); // stopped watching a download that carries on

	let file = $state<File | null>(null);
	// What the caller may do in the picked namespace, asked when it changes. A
	// missing answer or field reads no: Upload waits for a yes, and the
	// boot-image choice shows only where the route would take it.
	const canImage = $derived(!!(namespace && caps.ns(namespace)?.bootImageCreate));
	const uploadOff = $derived(caps.cluster?.imageUpload === false);
	const upload = $derived(
		uploadOff
			? { ok: false, why: "Image upload is off: this cluster's CDI publishes no upload proxy." }
			: caps.nsGate(
					namespace,
					(n) => n.dataVolumeCreate,
					`You may not upload images in ${namespace}.`,
				),
	);
	const fetchGate = $derived(imageGate.import(namespace));
	// A download is always a boot image: where the caller may not add one, the
	// URL and registry choices hide, unless the opener asked for one.
	const fetchOffered = $derived(
		!!given || !caps.ns(namespace) || !!caps.ns(namespace)?.bootImageImport,
	);
	// svelte-ignore state_referenced_locally
	let offerImage = $state(bootImage ?? true);
	const asBoot = $derived(downloads || (offerImage && canImage));
	let image = $state(''); // the boot image registered, "namespace/name"
	// Where the one this run registered lives: the fields stay editable after
	// a failure, so a delete names what was made, not what is typed now.
	const made = $derived.by(() => {
		const at = image.indexOf('/');
		return { namespace: image.slice(0, at), name: image.slice(at + 1) };
	});
	let gone = $state(''); // a boot image deleted here, so the form offers to add it again
	// svelte-ignore state_referenced_locally
	let address = $state(given?.source ?? '');
	// svelte-ignore state_referenced_locally
	let name = $state(given?.source ? suggestImageName(given.kind, given.source) : '');
	let nameEdited = false;
	// svelte-ignore state_referenced_locally
	let size = $state(given?.size || (given ? '20Gi' : '10Gi'));
	let storageClass = $state('');
	let dragging = $state(false);

	const classesRes = resource<StorageClassInfo[]>(() => 'storage-classes', api.storageClasses);
	const classRows = $derived(
		classesRes.data?.length ? classesRes.data : (inventory.options?.storageClasses ?? []),
	);
	const info = $derived(
		classesRes.data?.find((c) => c.name === (storageClass || inventory.defaultStorageClass)),
	);

	// The server's own checks, said before it is asked: http or https with a
	// host and no credentials, a registry reference with docker:// optional.
	const addressWhy = $derived.by(() => {
		const a = address.trim();
		if (!a) return source === 'url' ? 'Paste the URL' : 'Name the image reference';
		if (/\s/.test(a)) return 'The address may not contain spaces';
		if (source === 'url' && !/^https?:\/\/[^/?#]+/i.test(a))
			return 'The URL needs http:// or https:// and a host';
		if (source === 'url' && /^https?:\/\/[^/?#]*@/i.test(a))
			return 'The URL may not carry credentials';
		if (source === 'registry' && /:\/\//.test(a.replace(/^docker:\/\//i, '')))
			return 'A registry reference, like quay.io/containerdisks/fedora:42';
		return '';
	});
	function setAddress(v: string) {
		address = v;
		if (!nameEdited) name = suggestImageName(source, v);
	}
	function pick(s: Source) {
		source = s;
		if (s !== 'file' && !nameEdited) name = suggestImageName(s, address);
	}

	const sizeOK = $derived(/^\d+(Mi|Gi|Ti)$/.test(size));
	const bootTaken = $derived(
		(inventory.options?.osImages ?? []).some((i) => i.namespace === namespace && i.name === name),
	);
	const missing = $derived.by(() => {
		const m: string[] = [];
		// Rights first: no source is worth picking where adding it is refused.
		const gate = downloads ? fetchGate : upload;
		if (namespace && !gate.ok) m.push(gate.why);
		if (!downloads && !file) m.push('Pick an image file');
		if (downloads && addressWhy) m.push(addressWhy);
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

	const ORDER = $derived<Phase[]>(
		downloads
			? ['creating', 'preparing', 'importing', 'done']
			: ['creating', 'preparing', 'uploading', 'importing', 'done'],
	);
	const at = $derived(ORDER.indexOf(phase));
	const running = $derived(at >= 0 && phase !== 'done');
	// Closing loses an upload in flight, never a download: the cluster carries on.
	$effect(() => {
		sheet.pinned = running && !downloads;
	});

	// Torn down (sign-out, navigation): stop the stream and the polls. A
	// delete stops the polls of the run it removes (watch).
	const teardown = new AbortController();
	$effect(() => () => teardown.abort());
	let watch = new AbortController();
	const sleep = (ms: number) =>
		new Promise<void>((resolve, reject) => {
			const signal = AbortSignal.any([teardown.signal, watch.signal]);
			const abort = () => {
				clearTimeout(id);
				reject(signal.reason);
			};
			const id = setTimeout(() => {
				signal.removeEventListener('abort', abort);
				resolve();
			}, ms);
			signal.addEventListener('abort', abort, { once: true });
		});

	// Polls the DataVolume until pred holds; false when the watch ran out first.
	async function until(pred: (phase: string) => boolean): Promise<boolean> {
		for (let i = 0; i < 600; i++) {
			teardown.signal.throwIfAborted();
			watch.signal.throwIfAborted();
			const st = await api.uploadStatus(namespace, name);
			if (st.progress) importInfo = st.progress;
			importError = st.error ?? '';
			if (st.phase === 'Failed')
				throw new Error(
					`The cluster reported the ${downloads ? 'download' : 'upload'} failed${importError ? `: ${importError}` : ''}`,
				);
			if (pred(st.phase)) return true;
			await sleep(2000);
		}
		return false;
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

	async function sendFile() {
		const target = await api.createUpload({
			namespace,
			name,
			size,
			storageClass: storageClass || undefined,
			bootImage: asBoot,
		});
		image = target.image ?? '';
		phase = 'preparing';
		if (!(await until((p) => p === 'UploadReady')))
			throw new Error('Timed out waiting for storage to be ready');
		const { token, uploadUrl } = await api.uploadToken(namespace, name);
		phase = 'uploading';
		await stream(uploadUrl, token);
		phase = 'importing';
		if (!(await until((p) => p === 'Succeeded')))
			throw new Error('Timed out waiting for the import to finish');
	}

	// Watches the download until it lands: true once it has, false when the
	// watch ran out while it carries on.
	async function fetchSource(): Promise<boolean> {
		const target = await api.createImport({
			namespace,
			name,
			size,
			storageClass: storageClass || undefined,
			kind: source === 'registry' ? 'registry' : 'url',
			source: address.trim(),
		});
		image = target.image ?? '';
		phase = 'preparing';
		if (!(await until((p) => p === 'ImportInProgress' || p === 'Succeeded'))) return false;
		phase = 'importing';
		return until((p) => p === 'Succeeded');
	}

	async function start() {
		if (missing.length || running) return;
		error = '';
		importError = '';
		image = '';
		gone = '';
		armed = false;
		watch = new AbortController();
		try {
			phase = 'creating';
			if (downloads) still = !(await fetchSource());
			else await sendFile();
			phase = 'done';
			// The catalog New VM reads gains the image now, not at the next sign-in.
			if (image) void inventory.loadOptions(true);
		} catch (e) {
			if (e instanceof Unauthorized || teardown.signal.aborted || watch.signal.aborted) return;
			error = friendlyError(e);
			phase = 'error';
			// Its name stays taken until it is deleted: the form says so.
			if (image) void inventory.loadOptions(true);
		}
	}
	sheet.submit = start;

	// A boot image this sheet registered that never finished: one that failed,
	// or one the cluster keeps retrying. Deleting it frees its name and keeps
	// the form as typed, so a fix is one edit away.
	const leftover = $derived(!!image && (phase === 'error' || (running && !!importError)));
	const removal = action();
	const delGate = $derived(imageGate.delete(made.namespace));
	let armed = $state(false);
	async function remove() {
		if (!armed) {
			armed = true;
			return;
		}
		const { namespace: ns, name: n } = made;
		if (!(await removal.run(() => api.deleteImage(ns, n)))) return;
		watch.abort();
		armed = false;
		gone = n;
		image = '';
		error = '';
		importError = '';
		importInfo = '';
		phase = 'form';
		void inventory.loadOptions(true);
	}

	const STEPS = $derived<{ at: number; label: () => string }[]>(
		downloads
			? [
					{ at: 0, label: () => 'Creating the disk and the boot image' },
					{ at: 1, label: () => 'Preparing storage' },
					{
						at: 2,
						label: () =>
							`Downloading${phase === 'importing' && importInfo && importInfo !== 'N/A' ? `, ${importInfo}` : ''}`,
					},
				]
			: [
					{ at: 0, label: () => 'Creating the disk' },
					{ at: 1, label: () => 'Preparing storage' },
					{ at: 2, label: () => `Uploading${phase === 'uploading' ? `, ${pct}%` : ''}` },
					{
						at: 3,
						label: () => `Importing${phase === 'importing' && importInfo ? `, ${importInfo}` : ''}`,
					},
				],
	);

	const CHOICES: { id: Source; label: string; text: string }[] = [
		{ id: 'file', label: 'Upload a file', text: 'From this computer, straight to the cluster.' },
		{ id: 'url', label: 'URL', text: 'The cluster downloads it over http or https.' },
		{ id: 'registry', label: 'Registry', text: 'The cluster pulls a container disk image.' },
	];
	const choices = $derived(CHOICES.filter((c) => c.id === 'file' || fetchOffered));
</script>

{#snippet deleteIt()}
	<div class="flex flex-col gap-1.5">
		<div class="flex flex-wrap items-center gap-2">
			<Button
				variant={armed ? 'danger' : 'danger-secondary'}
				size="sm"
				disabled={removal.busy || !delGate.ok}
				title={delGate.why || undefined}
				onclick={remove}>{armed ? 'Confirm delete' : 'Delete it'}</Button
			>
			<StageChip kind="now" />
		</div>
		{#if !delGate.ok}<p class="text-xs leading-4 text-ink-faint">{delGate.why}</p>{/if}
		<Consequence tone="danger" size="sm">{imageDeleteConsequence(made.name, 0)}</Consequence>
		<ErrorNote error={removal.error} />
	</div>
{/snippet}

<SheetBody error={leftover ? '' : error}>
	{#snippet form()}
		{#if phase === 'form' || phase === 'error'}
			{#if leftover}
				<section
					aria-label="What failed"
					class="flex flex-col gap-2.5 rounded-lg border border-line px-3.5 py-3"
				>
					<ErrorNote {error} />
					<p class="text-[13px] text-ink-soft">
						<span class="font-mono font-medium text-ink">{made.name}</span> stays in
						<span class="font-mono">{made.namespace}</span> until you delete it.
					</p>
					{@render deleteIt()}
				</section>
			{:else if gone}
				<p class="rounded-lg border border-ok/40 bg-ok-soft px-3.5 py-2.5 text-[13px] text-ok-ink">
					Deleted <span class="font-mono font-medium">{gone}</span> and its disk. {downloads
						? 'Fix what was wrong and add it again.'
						: 'Upload it again.'}
				</p>
			{/if}
			{#if choices.length > 1}
				<fieldset class="min-w-0">
					<legend class="mb-2 {LABEL}">Where it comes from</legend>
					<div class="flex flex-col overflow-hidden rounded-lg border border-line">
						{#each choices as c (c.id)}
							{@const on = source === c.id}
							{@const off = c.id === 'file' && uploadOff}
							<label
								class="grid grid-cols-[16px_minmax(0,1fr)] items-start gap-x-3 gap-y-1 border-t border-line-soft px-3.5 py-3 first:border-t-0 {off
									? 'cursor-not-allowed'
									: 'cursor-pointer'} {on
									? 'bg-select-soft shadow-[inset_3px_0_0_var(--color-accent)]'
									: off
										? ''
										: 'hover:bg-inset'}"
							>
								<input
									type="radio"
									name="img-source"
									value={c.id}
									checked={on}
									disabled={off}
									onchange={() => pick(c.id)}
									class="mt-0.5 size-4 accent-accent"
								/>
								<span class="flex min-w-0 flex-wrap items-baseline gap-x-2.5">
									<b class="font-bold {off ? 'text-ink-faint' : 'text-ink'}">{c.label}</b>
									<span class="text-[13px] text-ink-muted">{off ? upload.why : c.text}</span>
								</span>
							</label>
						{/each}
					</div>
				</fieldset>
			{/if}

			{#if downloads}
				<div class="flex flex-col gap-1.5">
					<label class={LABEL} for="img-address"
						>{source === 'url' ? 'URL' : 'Image reference'}</label
					>
					<input
						id="img-address"
						class="{FIELD} font-mono"
						value={address}
						oninput={(e) => setAddress(e.currentTarget.value)}
						placeholder={source === 'url'
							? 'https://images.example.org/noble-server-cloudimg-amd64.img'
							: 'quay.io/containerdisks/fedora:42'}
						autocomplete="off"
						spellcheck="false"
						data-autofocus
					/>
					<span class={HELP}
						>The cluster pulls it itself, so it must be reachable from the cluster without
						credentials.{source === 'registry' ? ' docker:// is optional.' : ''}</span
					>
					{#if given?.redacted && address.trim() === given.source}
						<span class="{HELP} text-warn-ink!"
							>The disks' address carried a query string or credentials, left out here. Paste the
							full address if the server needs it.</span
						>
					{/if}
				</div>
			{:else}
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
			{/if}

			<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="img-name">Name</label>
					<input
						id="img-name"
						class="{FIELD} font-mono font-medium"
						value={name}
						oninput={(e) => {
							name = e.currentTarget.value;
							nameEdited = true;
						}}
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
				<span class={HELP}
					>At least the image's virtual size.{given?.size && size === given.size
						? ' What its disks asked for.'
						: ''}</span
				>
			</div>
			<ClassPick
				label="Storage class"
				value={storageClass}
				onpick={(c) => (storageClass = c)}
				classes={classRows}
			/>
			{#if canImage && !downloads}
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
					{#if active && importError}
						<div class="ml-7 flex flex-col gap-1.5">
							<Consequence tone="danger"
								>The cluster cannot {downloads ? 'download' : 'import'} it: {importError}</Consequence
							>
							<Consequence tone="info"
								>It retries on its own.{downloads
									? ' If the address is wrong, delete it and add it again.'
									: ''}</Consequence
							>
							{#if leftover}
								<div class="mt-1.5">{@render deleteIt()}</div>
							{/if}
						</div>
					{/if}
				{/each}
			</ol>
			{#if phase === 'done'}
				<p
					class="rounded-lg border px-3.5 py-2.5 text-[13px] {still
						? 'border-line bg-inset text-ink-soft'
						: 'border-ok/40 bg-ok-soft text-ok-ink'}"
				>
					<span class="font-mono font-medium">{name}</span>
					{#if still}
						is still downloading in <span class="font-mono">{namespace}</span>. The Images tab shows
						it ready once it lands.
					{:else}
						{image ? 'is a boot image in' : 'is ready in'}
						<span class="font-mono">{namespace}</span>.
					{/if}
				</p>
			{/if}
		{/if}
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path=""
			project={inventory.projectOf(namespace)}
			mode="now"
			merges={downloads
				? 'The cluster downloads it once; new VMs clone it.'
				: 'Uploads now, straight from your browser to the cluster. No pull request: an image is a disk, not config.'}
		>
			{#snippet consequences()}
				<Consequence tone="info"
					>A disk named {name || (downloads ? 'after the source' : 'after the file')} in {namespace ||
						'that namespace'}, ready once {downloads ? 'downloaded' : 'imported'}.</Consequence
				>
				{#if asBoot}
					<Consequence tone="ok">New VM lists it as a boot image.</Consequence>
				{/if}
				{#if info}
					<Consequence tone={liveMigrates(info) ? 'ok' : 'warn'}
						>{liveMigrates(info) ? 'Any host can read it.' : 'It stays on one host.'}</Consequence
					>
				{/if}
				{#if downloads}
					<Consequence>No pull request: an image is a disk, not config.</Consequence>
					<Consequence>You can close this while it downloads.</Consequence>
				{:else}
					<Consequence>Keep this tab open until it finishes.</Consequence>
				{/if}
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		{#if phase === 'done'}
			<span class="flex-1 text-ink-muted"
				>{still
					? 'Downloading in the cluster.'
					: downloads
						? 'Downloaded.'
						: 'Uploaded and imported.'}</span
			>
			{#if image && !still}
				<Button variant="secondary" class="h-9! px-3!" onclick={onclose}>Done</Button>
				<Button
					class="h-9! px-4!"
					onclick={() => (ui.modal = { kind: 'newVM', namespaces: null, namespace, image })}
					>New VM from it</Button
				>
			{:else}
				<Button class="h-9! px-4!" onclick={onclose}>Done</Button>
			{/if}
		{:else if running && downloads}
			<span class="flex-1 text-ink-muted"
				>{importError
					? 'The cluster retries it on its own. You can close this.'
					: 'The cluster is downloading it. You can close this.'}</span
			>
			<Button variant="secondary" class="h-9! px-3!" onclick={onclose}>Close</Button>
		{:else if running}
			<span class="flex-1 text-ink-muted">Working. Keep this tab open.</span>
		{:else}
			<SheetFooter
				label={downloads
					? phase === 'error'
						? 'Try again'
						: 'Add boot image'
					: phase === 'error'
						? 'Retry upload'
						: 'Upload image'}
				{missing}
				onsubmit={start}
				oncancel={onclose}
				note={downloads ? 'Starts as soon as you add it.' : 'Starts as soon as you upload.'}
			/>
		{/if}
	{/snippet}
</SheetBody>
