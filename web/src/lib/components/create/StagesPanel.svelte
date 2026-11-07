<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronDown, Code, FileText } from '@lucide/svelte';
	import { api, type CreateBodies, type CreateKind } from '#lib/api.js';
	import { resource } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import StageChip from '../StageChip.svelte';

	// "What this stages": the file, the repo, how it reaches the cluster, what
	// happens when it does, and the YAML itself on demand. The YAML comes from
	// the create route's dry run - the same render staging runs - fetched only
	// while it is open, so typing never posts the form.
	type Preview = { [K in CreateKind]: { kind: K; body: CreateBodies[K] } }[CreateKind];
	let {
		path,
		project,
		edit = false,
		mode = 'pr',
		merges = 'Created when the pull request merges.',
		preview = null,
		backing = '',
		consequences,
		extra,
	}: {
		path: string;
		project: string;
		edit?: boolean;
		mode?: 'pr' | 'now';
		merges?: string;
		preview?: Preview | null;
		backing?: string;
		consequences?: Snippet;
		extra?: Snippet;
	} = $props();

	let open = $state(false);
	const yaml = resource(
		() => (open && preview ? JSON.stringify(preview) : ''),
		(k) => {
			if (!k) return Promise.resolve(null);
			const p = JSON.parse(k) as Preview;
			return api.previewCreate(p.kind, p.body as never);
		},
	);
	// The dry run names the file better than the client can: a create of an
	// object git already declares replaces that file.
	const file = $derived(yaml.data?.files?.[0]);
	const shownPath = $derived(file?.path ?? path);
	const isEdit = $derived(file ? file.kind === 'edit' : edit);
	const repo = $derived(yaml.data?.project ?? project);

	// The change this one rides with: the caller's own draft in that repo.
	const staged = $derived(drafts.drafts.find((d) => d.project === repo)?.draft.items ?? []);
	const joins = $derived(
		!staged.length
			? ''
			: staged.length === 1
				? `Joins your staged change to ${staged[0].name}.`
				: `Joins your ${staged.length} staged changes.`,
	);
</script>

<h3 class="text-sm leading-5 font-bold text-ink">What this stages</h3>
{#if mode === 'pr'}
	<div class="flex min-w-0 items-start gap-2">
		<FileText size={16} class="mt-0.5 shrink-0 text-accent-ink" />
		<span class="min-w-0 font-mono text-sm font-medium text-ink"
			>{#each shownPath.split('/') as part, i (i)}{#if i}/<wbr />{/if}{part}{/each}</span
		>
	</div>
	<p class="-mt-1.5 text-[13px] text-ink-soft">
		{isEdit ? 'Changes this file in the' : 'New file in the'}
		<span class="font-mono font-medium">{repo}</span> repo
	</p>
	<div class="flex flex-col items-start gap-1">
		<StageChip kind="pr" />
		<span class="text-xs leading-4 text-ink-faint">{merges}</span>
		{#if joins}<span class="text-xs leading-4 text-ink-faint">{joins}</span>{/if}
	</div>
{:else}
	<div class="flex flex-col items-start gap-1">
		<StageChip kind="now" />
		<span class="text-xs leading-4 text-ink-faint">{merges}</span>
	</div>
{/if}
{#if extra}{@render extra()}{/if}
{#if consequences}
	<div class="h-px bg-line-soft"></div>
	<p class="text-[13px] leading-5 font-medium text-ink">
		{mode === 'pr' ? 'When it merges' : 'What happens'}
	</p>
	<div class="flex flex-col gap-2">{@render consequences()}</div>
{/if}
{#if preview}
	<div class="h-px bg-line-soft"></div>
	<div>
		<button
			type="button"
			aria-expanded={open}
			onclick={() => (open = !open)}
			class="flex h-8 w-full items-center gap-2 text-left text-sm font-medium text-ink"
		>
			<Code size={16} class="text-ink-muted" />Show YAML
			<ChevronDown
				size={16}
				class="ml-auto text-ink-muted transition-transform {open ? 'rotate-180' : ''}"
			/>
		</button>
		{#if backing}<p class="text-xs leading-4 text-ink-faint">Backed by {backing}</p>{/if}
		{#if open}
			{#if yaml.error}
				<p class="mt-2 text-xs text-warn-ink">{yaml.error}</p>
			{:else if !yaml.data}
				<p class="mt-2 text-xs text-ink-faint">Rendering...</p>
			{:else}
				{#each yaml.data.files as f (f.path)}
					{#if yaml.data.files.length > 1}
						<p class="mt-2 font-mono text-xs font-medium text-ink-soft">{f.path}</p>
					{/if}
					<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
					<pre
						tabindex="0"
						aria-label="YAML of {f.path}"
						class="mt-2 max-h-80 overflow-auto rounded-md border border-line bg-panel p-2.5 font-mono text-[11px] leading-4 text-ink-soft">{f.yaml}</pre>
				{/each}
			{/if}
		{/if}
	</div>
{:else if backing}
	<p class="text-xs leading-4 text-ink-faint">Backed by {backing}</p>
{/if}
