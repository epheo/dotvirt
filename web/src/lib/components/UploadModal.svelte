<script lang="ts">
	import { Check, Upload } from 'lucide-svelte';
	import { api, Unauthorized, type Options } from '$lib/api';
	import Modal from './Modal.svelte';

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
	// svelte-ignore state_referenced_locally
	let namespace = $state(namespaces[0] ?? '');
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
	const validName = $derived(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name) && name.length <= 63);
	const ready = $derived(!!file && validName && !!namespace && /^\d+(Mi|Gi|Ti)$/.test(size));

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
				<label class="block">
					<span class="text-ink-soft">Disk name</span>
					<input
						bind:value={name}
						placeholder="my-image"
						class="mt-1 w-full rounded border border-line-strong px-2 py-1.5 font-mono text-sm"
					/>
				</label>
				<label class="block">
					<span class="text-ink-soft">Project (namespace)</span>
					<select
						bind:value={namespace}
						class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
					>
						{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
					</select>
				</label>
				<label class="block">
					<span class="text-ink-soft">Disk size</span>
					<input
						bind:value={size}
						placeholder="10Gi"
						class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
					/>
				</label>
				<label class="block">
					<span class="text-ink-soft">Storage class</span>
					<select
						bind:value={storageClass}
						class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
					>
						<option value="">cluster default</option>
						{#each options?.storageClasses ?? [] as sc (sc.name)}
							<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
						{/each}
					</select>
				</label>
			</div>
			{#if file}
				<p class="mt-2 text-xs text-ink-faint">
					{file.name} · {(file.size / 1024 ** 2).toFixed(1)} MiB — ensure the disk size fits the image's
					virtual size.
				</p>
			{/if}
			{#if !validName && name}
				<p class="mt-1 text-xs text-warn-ink">
					Lowercase letters, digits and dashes only (≤63 chars).
				</p>
			{/if}
			{#if error}
				<pre
					class="mt-2 rounded bg-danger-soft/60 p-2 text-xs whitespace-pre-wrap text-danger-ink">{error}</pre>
			{/if}
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
					<p class="mt-3 rounded bg-green-50 p-3 text-xs text-green-800">
						<strong>{name}</strong> is ready in <strong>{namespace}</strong> — use it as a VM's boot disk.
					</p>
				{/if}
			</div>
		{/if}
	</div>
	{#snippet footer()}
		{#if stage === 'form' || stage === 'error'}
			<button
				onclick={onclose}
				class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong">Cancel</button
			>
			<button
				onclick={start}
				disabled={!ready}
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
