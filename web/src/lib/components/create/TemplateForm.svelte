<script lang="ts">
	import { untrack } from 'svelte';
	import { Check, TriangleAlert } from '@lucide/svelte';
	import { api, type DeployTemplateRequest, type Template } from '#lib/api.js';
	import { libraryLabel } from '#lib/catalog.js';
	import { action } from '#lib/resource.svelte.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Card from '../Card.svelte';
	import Consequence from '../Consequence.svelte';
	import PickList from './PickList.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, plural, useSheet } from './sheet.svelte.js';

	// A VM from a saved setup: pick the template, the place and a name, fill
	// what the template asks for. The server renders it with the engine the
	// native CRD will use, so Show YAML is exactly what stages.
	let {
		namespace = $bindable(),
		library = '',
		template = '',
		onclose,
	}: {
		namespace: string;
		library?: string;
		template?: string;
		onclose: () => void;
	} = $props();

	const sheet = useSheet();
	// svelte-ignore state_referenced_locally
	const opener = inventory.projectOf(namespace);
	const places = $derived(inventory.namespaces);
	$effect(() => {
		const list = places;
		untrack(() => {
			if (!list.includes(namespace)) namespace = list[0] ?? '';
		});
	});
	const project = $derived(inventory.projectOf(namespace));

	// The library re-pulls on open, so a template merged since the last visit
	// is deployable; ones that failed to parse are not offered.
	$effect(() => {
		catalog.load();
	});
	const templates = $derived(catalog.templates?.filter((t) => !t.error) ?? null);
	const key = (t: Template) => `${t.library}/${t.name}`;
	// svelte-ignore state_referenced_locally
	let picked = $state(library && template ? `${library}/${template}` : '');
	$effect(() => {
		const list = templates;
		untrack(() => {
			if (list?.length && !list.some((t) => key(t) === picked)) {
				// The project's own library first, then the shared one.
				picked = key(list.find((t) => t.library === project) ?? list[0]);
			}
		});
	});
	const tpl = $derived(templates?.find((t) => key(t) === picked) ?? null);
	let query = $state('');
	const shown = $derived(
		(templates ?? [])
			.filter(
				(t) =>
					!query ||
					`${t.name} ${t.description ?? ''} ${libraryLabel(t.library)}`
						.toLowerCase()
						.includes(query.trim().toLowerCase()),
			)
			.sort(
				(a, b) =>
					Number(b.library === project) - Number(a.library === project) ||
					a.name.localeCompare(b.name),
			),
	);

	let name = $state(''); // empty = the template's NAME default or a generated one
	let params = $state<Record<string, string>>({});
	$effect(() => {
		void picked;
		untrack(() => (params = {}));
	});
	const formParams = $derived((tpl?.parameters ?? []).filter((p) => p.name !== 'NAME'));
	const nameParam = $derived(tpl?.parameters?.find((p) => p.name === 'NAME') ?? null);
	const taken = $derived(
		new Set(
			inventory.projects
				.flatMap((p) => p.namespaces)
				.filter((n) => n.namespace === namespace)
				.flatMap((n) => n.vms.map((v) => v.name)),
		),
	);
	const nameOK = $derived(
		name === '' ? !!(nameParam?.value || nameParam?.generate) : validName(name) && !taken.has(name),
	);
	// A required parameter is satisfiable empty only when the template
	// generates or defaults it, as the engine enforces.
	const unset = $derived(
		formParams.filter((p) => p.required && !p.value && !p.generate && !params[p.name]?.trim()),
	);
	const secret = (n: string) => /password|secret/i.test(n);
	const long = (n: string) => /ssh|key|user_data/i.test(n);

	let powerOn = $state(true);

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!tpl) m.push('Pick a template');
		if (!namespace) m.push('Pick a project and namespace');
		if (!nameOK) m.push(name ? `${name} cannot be used here` : 'Name the VM');
		for (const p of unset) m.push(`Fill in ${p.displayName || p.name}`);
		return m;
	});
	const request = $derived.by((): DeployTemplateRequest => {
		const sent: Record<string, string> = {};
		for (const [k, v] of Object.entries(params)) if (v?.trim()) sent[k] = v;
		const req: DeployTemplateRequest = {
			library: tpl?.library ?? '',
			template: tpl?.name ?? '',
			namespace,
		};
		if (name.trim()) req.name = name.trim();
		if (Object.keys(sent).length) req.parameters = sent;
		if (powerOn) req.powerOn = true;
		return req;
	});

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		if (await op.run(() => api.deployTemplate(req))) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;
</script>

<SheetBody error={op.error || catalog.error}>
	{#snippet form()}
		{#if !templates}
			<p class="text-sm text-ink-faint">Loading templates...</p>
		{:else if !templates.length}
			<p class="text-sm text-ink-soft">
				No saved setups yet. Save one from a VM's menu (Save as template) and it shows up here.
			</p>
		{:else}
			<PickList
				label="Templates"
				bind:query
				items={shown}
				{key}
				selected={(t) => key(t) === picked}
				ontoggle={(t) => (picked = key(t))}
				search={templates.length > 6}
				note={query
					? `${shown.length} of ${templates.length} templates match.`
					: `${plural(templates.length, 'template')}. ${project}'s come first.`}
				limit={6}
			>
				{#snippet row(t)}
					<span class="flex items-baseline justify-between gap-3">
						<span class="font-mono text-sm font-medium text-ink">{t.name}</span>
						<span class="text-xs whitespace-nowrap text-ink-muted">{libraryLabel(t.library)}</span>
					</span>
					{#if t.description}<span class="block text-xs leading-4 text-ink-muted"
							>{t.description}</span
						>{/if}
				{/snippet}
			</PickList>
		{/if}

		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="tpl-name">Name</label>
				<input
					id="tpl-name"
					class="{FIELD} font-mono font-medium"
					bind:value={name}
					placeholder={nameParam?.value || 'Made when it stages'}
					autocomplete="off"
					spellcheck="false"
					data-autofocus
				/>
				<span class="{HELP} {name && !nameOK ? 'text-warn-ink!' : ''}">
					{#if name === ''}{nameParam?.generate
							? 'Left empty, a unique name is made when it stages.'
							: nameParam?.value
								? `Left empty, it is named ${nameParam.value}.`
								: 'Name it: the template has no name of its own.'}{:else if !validName(name)}<TriangleAlert
							size={13}
						/>{NAME_HINT}{:else if taken.has(name)}<TriangleAlert size={13} />Taken in {namespace}.{:else}<Check
							size={13}
							class="text-ok-ink"
						/><span class="text-ok-ink">Free.</span>{/if}
				</span>
			</div>
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="tpl-where">Project and namespace</label>
				<PlacePicker id="tpl-where" bind:value={namespace} namespaces={places} first={opener} />
				<span class={HELP}
					>{project === opener && opener ? 'Where you were. ' : ''}Search {plural(
						new Set(places.map((n) => inventory.projectOf(n))).size,
						'project',
					)}.</span
				>
			</div>
		</div>

		{#if tpl && formParams.length}
			<Card title="What it asks for" meta={plural(formParams.length, 'setting')}>
				<div class="flex flex-col gap-3">
					{#each formParams as p (p.name)}
						{@const need = p.required && !p.value && !p.generate}
						<div class="flex flex-col gap-1.5">
							<label class={LABEL} for="tpl-{p.name}"
								>{p.displayName || p.name}{#if need}<span class="font-normal text-ink-faint">
										(required)</span
									>{/if}</label
							>
							{#if long(p.name)}
								<textarea
									id="tpl-{p.name}"
									rows="2"
									class="{FIELD} h-auto! py-2 font-mono text-xs"
									bind:value={params[p.name]}
									placeholder={p.value || (p.generate ? 'Made when it stages' : '')}></textarea>
							{:else}
								<input
									id="tpl-{p.name}"
									type={secret(p.name) ? 'password' : 'text'}
									class={FIELD}
									bind:value={params[p.name]}
									placeholder={p.value || (p.generate ? 'Made when it stages' : '')}
								/>
							{/if}
							{#if p.description}<span class={HELP}>{p.description}</span>{/if}
						</div>
					{/each}
				</div>
			</Card>
		{/if}
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path="{namespace}/{name || nameParam?.value || '<generated>'}.yaml"
			{project}
			preview={missing.length ? null : { kind: 'template', body: request }}
		>
			{#snippet consequences()}
				{#if tpl}
					<Consequence tone="info"
						>Made from {tpl.name}{tpl.instancetype
							? `, size ${tpl.instancetype}`
							: ''}.</Consequence
					>
				{/if}
				{#if powerOn}
					<Consequence tone="ok">Starts as soon as it is created.</Consequence>
				{:else}
					<Consequence>Stays stopped until you start it.</Consequence>
				{/if}
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		<SheetFooter label="Stage VM" {missing} busy={op.busy} onsubmit={stage} oncancel={onclose}>
			{#snippet left()}
				<label class="flex cursor-pointer items-center gap-2">
					<input type="checkbox" class="size-4 accent-accent" bind:checked={powerOn} />
					Start {name || 'it'} when it merges
				</label>
			{/snippet}
		</SheetFooter>
	{/snippet}
</SheetBody>
