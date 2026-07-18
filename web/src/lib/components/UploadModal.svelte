<script lang="ts">
	import { Check, Upload } from 'lucide-svelte';
	import { api, Unauthorized, type Options } from '$lib/api';
	import { validName, NAME_HINT } from '$lib/validate';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import NamespaceSelect from './NamespaceSelect.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';

	// Image upload (OVF-import analog). dotvirt creates the upload-target
	// DataVolume + mints a token; the browser then streams the file STRAIGHT to
	// cdi-uploadproxy (open CORS), so multi-GB images never pass through dotvirt.
	// The proxy's cert is the cluster ingress CA — the same one serving this app,
	// so the browser already trusts it.
	let {
		namespaces,
		onclose,
		ondone,
	}: {
		namespaces: string[];
		onclose: () => void;
		ondone?: () => void;
	} = $props();

	type Stage = 'form' | 'creating' | 'preparing' | 'uploading' | 'importing' | 'done' | 'error';
	let stage = $state<Stage>('form');
	let error = $state('');
	let uploadPct = $state(0);
	let importInfo = $state('');

	let file = $state<File | null>(null);
	let name = $state('');
	let namespace = $state('');
	let size = $state('10Gi');
	let storageClass = $state('');
	let options = $state<Options | null>(null);

	$effect(() => {
		api
			.options()
			.then((o) => (options = o))
			.catch(() => {});
	});

	// RFC 1123 label (a PVC/DataVolume name), like the clone target.
	const nameOK = $derived(validName(name));
	const sizeOK = $derived(/^\d+(Mi|Gi|Ti)$/.test(size));
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!file) m.push('Pick an image file');
		if (!name) m.push('Disk name is required');
		else if (!nameOK) m.push('Disk name must be lowercase alphanumeric with dashes');
		if (!namespace) m.push('Project is required');
		if (!sizeOK) m.push('Disk size must be a quantity like 10Gi');
		return m;
	});
	const ready = $derived(missing.length === 0);

	function pickFile(e: Event) {
		const f = (e.target as HTMLInputElement).files?.[0] ?? null;
		file = f;
		if (f) {
			if (!name)
				name = f.name
					.replace(/\.[^.]+$/, '')
					.toLowerCase()
					.replace(/[^a-z0-9-]/g, '-');
			// Default the PVC a little larger than the file (qcow2 virtual size can
			// exceed the file); the user can adjust.
			const gi = Math.max(1, Math.ceil(f.size / 1024 ** 3) + 1);
			size = `${gi}Gi`;
		}
	}

	// Progress order, for the step checklist (which steps are done vs active).
	const STAGE_ORDER = ['creating', 'preparing', 'uploading', 'importing', 'done'];
	const stageIdx = $derived(STAGE_ORDER.indexOf(stage));

	const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

	// Poll the DataVolume until a predicate holds (or it fails / times out).
	async function pollUntil(pred: (phase: string) => boolean, label: string): Promise<void> {
		for (let i = 0; i < 600; i++) {
			const st = await api.uploadStatus(namespace, name);
			if (st.progress) importInfo = st.progress;
			if (st.phase === 'Failed') throw new Error('CDI reported the upload failed');
			if (pred(st.phase)) return;
			await sleep(2000);
		}
		throw new Error(`timed out waiting for ${label}`);
	}

	// Stream the file straight to cdi-uploadproxy with the token; XHR gives upload
	// progress an fetch() can't.
	function streamToProxy(url: string, token: string): Promise<void> {
		return new Promise((resolve, reject) => {
			const xhr = new XMLHttpRequest();
			xhr.open('POST', url);
			xhr.setRequestHeader('Authorization', `Bearer ${token}`);
			xhr.upload.onprogress = (e) => {
				if (e.lengthComputable) uploadPct = Math.round((e.loaded / e.total) * 100);
			};
			xhr.onload = () =>
				xhr.status >= 200 && xhr.status < 300
					? resolve()
					: reject(new Error(`proxy ${xhr.status}: ${xhr.responseText || xhr.statusText}`));
			xhr.onerror = () =>
				reject(
					new Error(
						'upload failed — the browser may not trust the cdi-uploadproxy certificate; open it once to accept it.',
					),
				);
			xhr.send(file);
		});
	}

	async function start() {
		if (!ready || !file) return;
		error = '';
		try {
			stage = 'creating';
			await api.createUpload({ namespace, name, size, storageClass: storageClass || undefined });
			stage = 'preparing';
			await pollUntil((p) => p === 'UploadReady', 'storage to be ready');
			const { token, uploadUrl } = await api.uploadToken(namespace, name);
			stage = 'uploading';
			await streamToProxy(uploadUrl, token);
			stage = 'importing';
			await pollUntil((p) => p === 'Succeeded', 'CDI to finish importing');
			stage = 'done';
			ondone?.();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
			stage = 'error';
		}
	}
</script>

<Modal title="Upload image" size="lg" dismissable={stage !== 'uploading'} {onclose}>
	{#snippet icon()}<Upload size={16} />{/snippet}
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm">
		{#if stage === 'form' || stage === 'error'}
			<p class="mb-3 text-xs text-ink-muted">
				Uploads a disk image (qcow2/raw/iso) as a DataVolume your VMs can boot from. The file
				streams straight from your browser to the cluster's upload proxy.
			</p>
			<div class="grid grid-cols-2 gap-4">
				<label class="col-span-2 block">
					<span class="text-ink-soft">Image file</span>
					<input
						type="file"
						onchange={pickFile}
						accept=".qcow2,.img,.raw,.iso,.gz,.xz"
						class="mt-1 w-full rounded border border-line-strong px-2 py-1.5 text-xs"
					/>
				</label>
				<FormField label="Disk name" error={name && !nameOK ? NAME_HINT : ''}>
					<TextInput bind:value={name} placeholder="my-image" mono />
				</FormField>
				<NamespaceSelect bind:namespace {namespaces} />
				<FormField label="Disk size" error={size && !sizeOK ? 'A quantity like 10Gi.' : ''}>
					<TextInput bind:value={size} placeholder="10Gi" mono />
				</FormField>
				<FormField label="Storage class">
					<SelectInput bind:value={storageClass}>
						<option value="">cluster default</option>
						{#each options?.storageClasses ?? [] as sc (sc.name)}
							<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
						{/each}
					</SelectInput>
				</FormField>
			</div>
			{#if file}
				<p class="mt-2 text-xs text-ink-faint">
					{file.name} · {(file.size / 1024 ** 2).toFixed(1)} MiB — ensure the disk size fits the image's
					virtual size.
				</p>
			{/if}
			<ErrorNote {error} class="mt-2" />
		{:else}
			<!-- Progress view. -->
			<div class="space-y-3 py-2">
				{#snippet step(label: string, active: boolean, complete: boolean)}
					<div class="flex items-center gap-2 text-sm">
						<span
							class="flex h-4 w-4 items-center justify-center rounded-full text-[10px] {complete
								? 'bg-ok text-white'
								: active
									? 'bg-accent text-white'
									: 'bg-line text-ink-faint'}"
						>
							{#if complete}<Check size={10} />{/if}
						</span>
						<span class={active || complete ? 'text-ink' : 'text-ink-faint'}>{label}</span>
					</div>
				{/snippet}
				{@render step('Creating target', stage === 'creating', stageIdx > 0)}
				{@render step('Preparing storage', stage === 'preparing', stageIdx > 1)}
				{@render step(
					`Uploading${stage === 'uploading' ? ` — ${uploadPct}%` : ''}`,
					stage === 'uploading',
					stageIdx > 2,
				)}
				{#if stage === 'uploading'}
					<div class="ml-6 h-2 overflow-hidden rounded-full bg-inset-strong">
						<div class="h-full rounded-full bg-accent" style="width:{uploadPct}%"></div>
					</div>
				{/if}
				{@render step(
					`Importing${stage === 'importing' && importInfo ? ` — ${importInfo}` : ''}`,
					stage === 'importing',
					stageIdx > 3,
				)}

				{#if stage === 'done'}
					<p class="mt-3 rounded bg-ok-soft/60 p-3 text-xs text-ok-ink">
						<strong>{name}</strong> is ready in <strong>{namespace}</strong> — use it as a VM's boot disk.
					</p>
				{/if}
			</div>
		{/if}
	</div>
	{#snippet footer()}
		{#if stage === 'form' || stage === 'error'}
			{#if !ready && missing.length}
				<span class="min-w-0 truncate text-xs text-warn-ink"
					>{missing[0]}{missing.length > 1 ? ` (+${missing.length - 1} more)` : ''}</span
				>
			{/if}
			<button
				onclick={onclose}
				class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong"
				>Cancel</button
			>
			<button
				onclick={start}
				disabled={!ready}
				title={ready ? '' : missing[0]}
				class="rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-line-strong"
			>
				{stage === 'error' ? 'Retry' : 'Upload'}
			</button>
		{:else if stage === 'done'}
			<button
				onclick={onclose}
				class="ml-auto rounded bg-accent px-4 py-1.5 text-sm font-medium text-white">Done</button
			>
		{:else}
			<span class="ml-auto text-xs text-ink-faint">Working… keep this tab open.</span>
		{/if}
	{/snippet}
</Modal>
