<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Desktops } from '#lib/api.js';
	import type { Tone } from '#lib/status.js';
	import { desktops, nothingReadable } from '#lib/state/desktops.svelte.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import Button from '#lib/components/Button.svelte';
	import Note from '#lib/components/Note.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import TabBar from '#lib/components/TabBar.svelte';

	// Every Desktops page: the section's chrome over the one pull, rendered
	// only once every kind the caller reads landed its first list, so an empty
	// list means none. A caller who may read none is told so, never left
	// reading.
	let {
		trail,
		title,
		status,
		context,
		tabs,
		tab = '',
		children,
	}: {
		trail: { label: string; href?: string }[];
		title: string;
		status?: { tone: Tone; text: string };
		context?: string;
		tabs?: { id: string; label: string; count?: number }[];
		tab?: string;
		children: Snippet<[Desktops]>;
	} = $props();

	const data = $derived(desktops.value);
	const header = $derived(
		status && {
			tone: status.tone === 'neutral' ? ('off' as const) : status.tone,
			text: status.text,
		},
	);
</script>

<div class="min-h-0 flex-1 overflow-y-auto bg-canvas">
	<div class="flex min-h-full flex-col gap-[18px] px-7 pt-5 pb-8">
		<div class="flex flex-col gap-2">
			<Breadcrumb {trail} />
			<PageHeader {title} status={header} {context} />
			{#if tabs}<TabBar class="mt-1" {tabs} active={tab} href={(id) => `?tab=${id}`} />{/if}
		</div>

		{#if data?.synced}
			{#if data.stale}
				<Note tone="warn" border>
					These may be out of date: the console lost its watch on the cluster. They refresh once it
					is back.
				</Note>
			{/if}
			{#if desktops.error}
				<Note tone="warn" border class="flex items-center gap-3">
					<span>Could not refresh: {desktops.error}</span>
					<Button variant="link" size="sm" onclick={() => desktops.pull()}>Try again</Button>
				</Note>
			{/if}
			{@render children(data)}
		{:else if desktops.error}
			<Note tone="danger" border class="flex items-center gap-3">
				<span>The desktops could not be read: {desktops.error}</span>
				<Button variant="link" size="sm" onclick={() => desktops.pull()}>Try again</Button>
			</Note>
		{:else if nothingReadable()}
			<div class="rounded-lg border border-line bg-panel px-card py-6 text-center text-[13px]">
				<p class="text-ink-muted">No desktop is shown to you.</p>
				<p class="text-ink-faint">A project admin can let you read a project's pools.</p>
				<p class="text-ink-faint">A platform admin can let you read its desktops.</p>
			</div>
		{:else if data}
			<p class="text-[13px] text-ink-muted">Reading the desktops.</p>
		{:else}
			<Skeleton rows={4} class="h-8" />
		{/if}
	</div>
</div>
