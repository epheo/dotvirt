<script lang="ts">
	import { untrack } from 'svelte';
	import { BookCopy } from 'lucide-svelte';
	import { api, Unauthorized, type Template } from '$lib/api';
	import Wizard from './Wizard.svelte';

	// Deploy from Template: pick a template + target, fill its parameters (the
	// Customization-Spec moment), review, stage. The render happens server-side
	// with the same engine the native CRD will use; the result lands in the
	// Changes drawer and applies when the project's PR merges.
	let {
		namespaces,
		library = '',
		template = '',
		onclose,
		onstaged
	}: {
		namespaces: string[]; // repo-backed target namespaces
		library?: string; // preselected library (from the Catalog's Deploy button)
		template?: string;
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let templates = $state<Template[] | null>(null);
	let loadError = $state('');

	// The preselection seeds from the opener's intent; the picker can change it.
	// svelte-ignore state_referenced_locally
	let pickedKey = $state(library && template ? `${library}/${template}` : '');
	let namespace = $state('');
	let name = $state(''); // empty = the template's NAME default (often generated)
	let powerOn = $state(false); // templates blueprint Halted; this boots the VM on sync
	let params = $state<Record<string, string>>({});
	let step = $state(0);
	let busy = $state(false);
	let error = $state('');

	$effect(() => {
		if (!namespace) namespace = namespaces[0] ?? '';
		untrack(() =>
			api
				.templates()
				.then((t) => {
					templates = t.templates.filter((x) => !x.error);
					if (!pickedKey && templates.length) pickedKey = key(templates[0]);
				})
				.catch((e) => {
					if (e instanceof Unauthorized) return;
					loadError = String(e);
				})
		);
	});

	const key = (t: Template) => `${t.library}/${t.name}`;
	const libraryLabel = (lib: string) => (lib === 'platform' ? 'Shared library' : lib);
	const tpl = $derived(templates?.find((t) => key(t) === pickedKey) ?? null);
	const libraries = $derived([...new Set((templates ?? []).map((t) => t.library))]);

	// Parameter changes reset with the template; NAME is handled by the name
	// field, everything else becomes a form input.
	$effect(() => {
		pickedKey;
		params = {};
	});
	const formParams = $derived((tpl?.parameters ?? []).filter((p) => p.name !== 'NAME'));
	const nameParam = $derived(tpl?.parameters?.find((p) => p.name === 'NAME') ?? null);

	// A client-side EXAMPLE of a generate-expression value — presentation only,
	// the server mints the real one at deploy time. Supports the documented
	// "[class]{n}" grammar; anything fancier just shows the raw pattern.
	function exampleFrom(pattern: string): string {
		return pattern.replace(/\[([^\]]+)\]\{(\d+)\}/g, (_, cls: string, n: string) => {
			let chars = cls
				.replace(/\\w/g, 'a-zA-Z0-9_')
				.replace(/\\d/g, '0-9')
				.replace(/\\a/g, 'a-zA-Z');
			const pool: string[] = [];
			for (let i = 0; i < chars.length; i++) {
				if (chars[i + 1] === '-' && chars[i + 2]) {
					for (let c = chars.charCodeAt(i); c <= chars.charCodeAt(i + 2); c++)
						pool.push(String.fromCharCode(c));
					i += 2;
				} else pool.push(chars[i]);
			}
			let out = '';
			for (let i = 0; i < Number(n); i++) out += pool[Math.floor(Math.random() * pool.length)];
			return out;
		});
	}
	const nameExample = $derived(
		nameParam?.generate && nameParam.from ? exampleFrom(nameParam.from) : ''
	);

	const validName = (s: string) => /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(s) && s.length <= 63;
	const nameOK = $derived(
		name === '' ? !!(nameParam?.value || nameParam?.generate) : validName(name)
	);
	const targetOK = $derived(!!tpl && !!namespace && nameOK);
	// A required parameter is satisfiable empty only when the template generates
	// or defaults it — mirroring the engine's own enforcement.
	const missing = $derived(
		formParams.filter((p) => p.required && !p.value && !p.generate && !params[p.name]?.trim())
	);
	const secret = (n: string) => /password|secret/i.test(n);
	const long = (n: string) => /ssh|key|user_data/i.test(n);

	async function deploy() {
		if (!tpl) return;
		busy = true;
		error = '';
		try {
			const sent: Record<string, string> = {};
			for (const [k, v] of Object.entries(params)) if (v.trim() !== '') sent[k] = v;
			await api.deployTemplate({
				library: tpl.library,
				template: tpl.name,
				namespace,
				name: name.trim() || undefined,
				parameters: Object.keys(sent).length ? sent : undefined,
				powerOn: powerOn || undefined
			});
			onstaged();
			onclose();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
		} finally {
			busy = false;
		}
	}
</script>

<Wizard
	title="Deploy from Template"
	bind:current={step}
	canFinish={targetOK && missing.length === 0}
	submitting={busy}
	{error}
	finishLabel="Stage deploy"
	footerHint="Stages into Changes — the VM is created when the project’s PR merges."
	{onclose}
	onfinish={deploy}
	steps={[
		{ title: 'Template & target', valid: targetOK, body: targetStep },
		{
			title: 'Guest customization',
			valid: formParams.length ? missing.length === 0 : undefined,
			body: customizeStep
		},
		{ title: 'Review', body: reviewStep }
	]}
>
	{#snippet icon()}<BookCopy size={16} class="text-ink-muted" />{/snippet}
</Wizard>

{#snippet targetStep()}
	{#if loadError}
		<p class="rounded bg-red-50 px-3 py-2 text-xs text-red-700">{loadError}</p>
	{:else if !templates}
		<p class="text-sm text-ink-faint">Loading templates…</p>
	{:else if !templates.length}
		<p class="text-sm text-ink-faint">
			No templates yet — save one from a VM (Clone to Template) or commit VirtualMachineTemplate
			manifests under templates/.
		</p>
	{:else}
		<div class="max-w-md space-y-3">
			<label class="block">
				<span class="mb-1 block text-xs font-medium text-ink-muted">Template</span>
				<select
					bind:value={pickedKey}
					class="w-full rounded border border-line px-2 py-1.5 text-sm"
				>
					{#each libraries as lib (lib)}
						<optgroup label={libraryLabel(lib)}>
							{#each templates.filter((t) => t.library === lib) as t (key(t))}
								<option value={key(t)}>{t.name}</option>
							{/each}
						</optgroup>
					{/each}
				</select>
				{#if tpl?.description}<p class="mt-1 text-xs text-ink-faint">{tpl.description}</p>{/if}
			</label>
			<label class="block">
				<span class="mb-1 block text-xs font-medium text-ink-muted">Target namespace</span>
				<select
					bind:value={namespace}
					class="w-full rounded border border-line px-2 py-1.5 text-sm"
				>
					{#each namespaces as ns (ns)}
						<option value={ns}>{ns}</option>
					{/each}
				</select>
			</label>
			<label class="block">
				<span class="mb-1 block text-xs font-medium text-ink-muted">VM name</span>
				<input
					bind:value={name}
					placeholder={nameExample ? `Auto-generate (e.g. ${nameExample})` : 'Auto-generate'}
					class="w-full rounded border border-line px-2 py-1.5 font-mono text-sm"
				/>
				<p class="mt-1 text-xs text-ink-faint">
					{#if name === ''}
						{nameExample
							? 'Left empty, a unique name is generated on deploy (the example above is illustrative).'
							: 'Left empty, the template’s default name is used.'}
					{:else if !validName(name)}
						<span class="text-amber-600">Lowercase alphanumeric and “-”, max 63 characters.</span>
					{/if}
				</p>
			</label>
		</div>
	{/if}
{/snippet}

{#snippet customizeStep()}
	{#if !formParams.length}
		<p class="text-sm text-ink-faint">This template has no parameters beyond the VM name.</p>
	{:else}
		<div class="max-w-md space-y-3">
			{#each formParams as p (p.name)}
				<label class="block">
					<span class="mb-1 block text-xs font-medium text-ink-muted">
						{p.displayName || p.name}
						{#if p.required && !p.value && !p.generate}<span class="text-red-600">*</span>{/if}
					</span>
					{#if long(p.name)}
						<textarea
							bind:value={params[p.name]}
							rows="3"
							placeholder={p.value || (p.generate ? 'generated on deploy' : '')}
							class="w-full rounded border border-line px-2 py-1.5 font-mono text-xs"></textarea>
					{:else}
						<input
							type={secret(p.name) ? 'password' : 'text'}
							bind:value={params[p.name]}
							placeholder={p.value || (p.generate ? 'generated on deploy' : '')}
							class="w-full rounded border border-line px-2 py-1.5 text-sm"
						/>
					{/if}
					{#if p.description}<p class="mt-1 text-xs text-ink-faint">{p.description}</p>{/if}
				</label>
			{/each}
		</div>
	{/if}
{/snippet}

{#snippet reviewStep()}
	{#if tpl}
		<dl class="max-w-md divide-y divide-slate-100 text-[13px]">
			<div class="flex justify-between gap-3 py-1.5">
				<dt class="text-ink-muted">Template</dt>
				<dd class="text-ink">{libraryLabel(tpl.library)} / {tpl.name}</dd>
			</div>
			<div class="flex justify-between gap-3 py-1.5">
				<dt class="text-ink-muted">Target</dt>
				<dd class="font-mono text-xs text-ink">{namespace}/{name.trim() || '(generated)'}</dd>
			</div>
			{#each formParams as p (p.name)}
				<div class="flex justify-between gap-3 py-1.5">
					<dt class="text-ink-muted">{p.displayName || p.name}</dt>
					<dd class="font-mono text-xs text-ink">
						{#if secret(p.name) && params[p.name]?.trim()}••••••••
						{:else}{params[p.name]?.trim() || p.value || (p.generate ? '(generated)' : '—')}{/if}
					</dd>
				</div>
			{/each}
		</dl>
		<label class="mt-3 flex max-w-md items-center gap-2 text-[13px] text-ink">
			<input type="checkbox" bind:checked={powerOn} class="accent-accent" />
			Power on after deployment
		</label>
	{/if}
{/snippet}
