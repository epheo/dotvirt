<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import type { Template } from '#lib/api.js';
	import { libraryLabel, templateKey, templatesFor } from '#lib/catalog.js';
	import { plural } from '#lib/format.js';
	import { projectNS } from '#lib/gates.js';
	import { DEFAULT_PAGE_SIZE, pageFor, pageSlice } from '#lib/paging.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { shortName } from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';
	import TemplatePanel from './TemplatePanel.svelte';

	// The templates new VMs are made from, kept in git: every library the
	// caller reads at the Compute root, a project's own plus the shared one on
	// its page. The open template rides ?item= so a shared link opens it.
	let { project }: { project?: string } = $props();

	// Re-pulled when a merged PR lands (tasksVersion): a template committed
	// through the app appears without a reload.
	$effect(() => {
		inventory.tasksVersion;
		catalog.load();
	});

	const rows = $derived(catalog.templates && templatesFor(catalog.templates, project));
	const invalid = $derived(rows?.filter((t) => t.error).length ?? 0);
	// A VM stages into a namespace the caller authors in; the sheet then asks
	// vmGate.create of the one picked.
	const canCreate = $derived(inventory.namespaces.length > 0);

	// A template opened from a link lands on the page that holds it.
	const picked = $derived(page.url.searchParams.get('item'));
	const keys = $derived(rows?.map(templateKey) ?? []);
	const size = $derived(Number(page.url.searchParams.get('per')) || DEFAULT_PAGE_SIZE);
	const s = $derived(
		pageSlice(
			keys.length,
			Number(page.url.searchParams.get('page')) || pageFor(keys, picked, size),
			size,
		),
	);
	const pageRows = $derived(rows?.slice(s.start, s.end) ?? []);
	const open = $derived(rows?.find((t) => templateKey(t) === picked) ?? null);

	// Paging and the open item are replaceState, like tabs: back never walks
	// them. Two moves in one handler compose (see StorageWorkspace).
	let pending: URLSearchParams | null = null;
	function go(params: Record<string, string | undefined>) {
		const q = new URLSearchParams(pending ?? page.url.search);
		for (const [k, v] of Object.entries(params)) {
			if (v) q.set(k, v);
			else q.delete(k);
		}
		pending = q;
		goto(`${page.url.pathname}?${q}`, { replace: true, reset: false }).finally(() => {
			if (pending === q) pending = null;
		});
	}
	const toggle = (key: string) => go({ item: picked === key ? undefined : key });

	// From a project's page the sheet starts in that project.
	const home = $derived(
		project
			? projectNS(inventory.projects.find((p) => p.name === project) ?? { namespaces: [] })
			: '',
	);
	const deploy = (t: Template) =>
		(ui.modal = {
			kind: 'deployTemplate',
			library: t.library,
			template: t.name,
			namespace: home || undefined,
		});

	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5';
</script>

{#snippet broken()}
	<StatusPill
		tone="warn"
		label="{plural(invalid, 'template')} {invalid === 1 ? 'does' : 'do'} not parse"
	/>
{/snippet}

{#if catalog.error}
	<div class="mb-4 flex flex-col items-start gap-2">
		<ErrorNote error={catalog.error} class="w-full" />
		<Button variant="secondary" size="sm" onclick={() => catalog.load()}>Try again</Button>
	</div>
{/if}

<!-- A failed read with nothing to show is the error alone, never a table that
     reads as loading. -->
{#if !catalog.error || rows}
	<div class="flex flex-wrap items-start gap-4">
		<Card
			class="flex-[2_1_640px]"
			title="VM templates"
			meta="In git, under templates/ in each library repo"
			actions={invalid ? broken : undefined}
			flush
		>
			<div class="overflow-x-auto">
				<table class="w-full border-collapse text-sm">
					<thead>
						<tr>
							<th scope="col" class="{TH} pl-card">Template</th>
							<th scope="col" class={TH}>Library</th>
							<th scope="col" class={TH}>Instance type</th>
							<th scope="col" class={TH}>Boot image</th>
							{#if canCreate}
								<th scope="col" class="{TH} pr-card"><span class="sr-only">Actions</span></th>
							{/if}
						</tr>
					</thead>
					<tbody class="tabular-nums">
						{#each pageRows as t (templateKey(t))}
							{@const key = templateKey(t)}
							{@const current = key === picked}
							<tr
								class="cursor-pointer {current
									? 'bg-select shadow-[inset_3px_0_0_var(--color-accent)]'
									: 'hover:bg-select-soft'}"
								onclick={() => toggle(key)}
							>
								<td class="{TD} pl-card">
									<button
										class="text-left font-mono font-medium hover:text-accent-ink"
										aria-expanded={current}
										onclick={(e) => {
											e.stopPropagation();
											toggle(key);
										}}>{t.name}</button
									>
									{#if t.error}
										<span class="ml-1.5"
											><StatusPill tone="warn" label="Does not parse" title={t.error} /></span
										>
									{/if}
									{#if t.description}
										<span class="block text-xs leading-4 text-ink-faint">{t.description}</span>
									{/if}
								</td>
								<td class="{TD} text-ink-muted">{libraryLabel(t.library)}</td>
								<td class="{TD} font-mono text-[13px] text-ink-muted">{t.instancetype || '-'}</td>
								<td class="{TD} font-mono text-[13px] text-ink-muted"
									>{t.image ? shortName(t.image) : '-'}</td
								>
								{#if canCreate}
									<td class="{TD} pr-card">
										<div class="flex justify-end gap-1.5">
											<Button
												variant="outline"
												size="sm"
												aria-label="Create VM from {t.name}"
												disabled={!!t.error}
												title={t.error ? 'It does not parse. Fix its file first.' : ''}
												onclick={(e) => {
													e.stopPropagation();
													deploy(t);
												}}>Create VM</Button
											>
										</div>
									</td>
								{/if}
							</tr>
						{:else}
							<tr>
								<td colspan={canCreate ? 5 : 4} class="px-card py-6 text-center text-ink-faint">
									{#if rows === null}
										Reading the template libraries.
									{:else}
										No templates yet. Clone a VM to one from its menu (Clone to Template), or commit
										VirtualMachineTemplate manifests under templates/ in a library repo.
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			<Pager
				class="border-t border-line px-card py-3"
				total={s.total}
				bind:page={() => s.page, (n) => go({ page: n > 1 ? String(n) : undefined })}
				bind:size={() => size, (n) => go({ per: n === DEFAULT_PAGE_SIZE ? undefined : String(n) })}
			/>
			{#if canCreate}
				<div
					class="flex items-start gap-2 border-t border-line-soft px-card py-3.5 text-xs leading-5 text-ink-faint"
				>
					<StageChip kind="pr" />
					<span class="min-w-0"
						>Create VM opens the create sheet with the template picked. Staging it opens a pull
						request in the chosen project.</span
					>
				</div>
			{/if}
		</Card>

		{#if open}
			<TemplatePanel
				template={open}
				oncreate={canCreate ? () => deploy(open) : undefined}
				onclose={() => go({ item: undefined })}
			/>
		{/if}
	</div>
{/if}
