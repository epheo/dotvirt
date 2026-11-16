<script lang="ts">
	import { branding } from '#lib/state/branding.svelte.js';
	import { isConsoleLink } from '#lib/branding.js';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import {
		Check,
		ChevronDown,
		CircleCheck,
		Monitor,
		Moon,
		Sun,
		TriangleAlert,
		User as UserIcon,
	} from '@lucide/svelte';
	import { dispatchVMAction } from '#lib/actions.js';
	import { hrefForScope, vmHref } from '#lib/nav.js';
	import { TONE_PILL } from '#lib/status.js';
	import IssueRow from './IssueRow.svelte';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { session } from '#lib/state/session.svelte.js';
	import { theme } from '#lib/state/theme.svelte.js';
	import GlobalSearch, { type SearchHit } from './GlobalSearch.svelte';
	import HeaderMenu from './HeaderMenu.svelte';
	import MenuItem from './MenuItem.svelte';
	import NewMenu from './NewMenu.svelte';

	// Global search: a hit either opens a VM or focuses its scope.
	function onSearchPick(hit: SearchHit) {
		switch (hit.kind) {
			case 'action':
				dispatchVMAction(hit.action, hit.vm);
				break;
			case 'vm':
				goto(vmHref(hit.vm.namespace, hit.vm.name));
				break;
			case 'project':
				goto(hrefForScope({ kind: 'project', project: hit.project }));
				break;
			case 'namespace':
				goto(hrefForScope({ kind: 'namespace', project: hit.project, namespace: hit.namespace }));
				break;
			case 'node':
				goto(hrefForScope({ kind: 'node', node: hit.node }));
				break;
			case 'network':
				goto(hrefForScope({ kind: 'network', network: hit.network }));
				break;
			case 'storage':
				goto(hrefForScope({ kind: 'storage', storageClass: hit.storageClass }));
				break;
		}
	}

	// The issues count: derived from the live stream and the alerts poll, so it
	// moves with the same frames the tree and grid repaint on.
	const issues = $derived(inventory.issues);
	const suggestions = $derived(inventory.suggestions);
	const worstTone = $derived(issues.some((i) => i.severity === 'danger') ? 'danger' : 'warn');

	// A link icon's tooltip sits under the bar whatever the item's height:
	// both are centred in it, so half the bar (24px) and a 4px gap below.
	const TIP =
		'pointer-events-none absolute top-[calc(50%+28px)] right-0 z-50 hidden rounded bg-side px-2 py-1 text-xs whitespace-nowrap text-side-ink shadow-lg';
	// Escape hides the tooltips without moving the pointer or focus (WCAG
	// 1.4.13); they come back once the pointer has left the items, or on the
	// next keyboard focus of one.
	let tipsOff = $state(false);
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') tipsOff = true;
	}}
	onpointerover={(e) => {
		if (tipsOff && !(e.target instanceof Element && e.target.closest('[data-tip]')))
			tipsOff = false;
	}}
/>

<!-- The bar stays dark in both themes, so its chrome uses the rail's side-*
     tokens, not ink tokens. -->
<header class="flex h-12 shrink-0 items-center gap-4 bg-bar px-4 text-white">
	<a href="/compute" class="flex items-center gap-2 text-[17px] font-bold">
		{#if branding.value?.logo}
			<img src="/api/branding/logo" alt="" class="h-6 w-auto" />
		{:else}
			<span class="size-2.5 rounded-full bg-brand" aria-hidden="true"></span>
		{/if}{branding.name}
	</a>

	<GlobalSearch onpick={onSearchPick} />

	<NewMenu />

	<div class="flex-1"></div>

	<!-- A provider's links back to its own app. An icon link names itself by
	     its label, shown as a tooltip on hover and on keyboard focus alike. The
	     one leading to this console is the current item, marked, not a link:
	     nothing to act on, so no tab stop; its label stays readable as text. -->
	{#each branding.value?.links ?? [] as l, i (i)}
		{#if isConsoleLink(l.url, page.url.origin)}
			<span
				aria-current="page"
				data-tip
				class="group relative flex items-center border-b-2 border-accent pb-0.5 {l.icon
					? ''
					: 'text-[13px] text-white'}"
			>
				{#if l.icon}
					<img
						src="/api/branding/icon/{encodeURIComponent(l.icon)}"
						alt=""
						class="size-[22px] object-contain"
					/>
					<span class="sr-only">{l.label}</span>
					<span aria-hidden="true" class="{TIP} {tipsOff ? '' : 'group-hover:block'}"
						>{l.label}</span
					>
				{:else}{l.label}{/if}
			</span>
		{:else if l.icon}
			<a
				href={l.url}
				target="_blank"
				rel="noopener noreferrer"
				aria-label={l.label}
				data-tip
				onfocus={() => (tipsOff = false)}
				class="group relative flex items-center opacity-80 hover:opacity-100 focus-visible:opacity-100"
			>
				<img
					src="/api/branding/icon/{encodeURIComponent(l.icon)}"
					alt=""
					class="size-[22px] object-contain"
				/>
				<span
					aria-hidden="true"
					class="{TIP} {tipsOff ? '' : 'group-hover:block group-focus-visible:block'}"
					>{l.label}</span
				>
			</a>
		{:else}
			<a
				href={l.url}
				target="_blank"
				rel="noopener noreferrer"
				class="text-[13px] text-side-dim hover:text-white">{l.label}</a
			>
		{/if}
	{/each}

	<!-- Issues: standing problems and firing alerts (never pending applies), one
	     count, named in words so a clean fleet reads as a stated fact. -->
	<HeaderMenu align="right">
		{#snippet trigger({ open, toggle })}
			<button
				onclick={toggle}
				aria-expanded={open}
				title="Issues: standing problems and firing alerts in the visible inventory"
				class="flex h-7 items-center gap-1.5 rounded-full px-2.5 text-[13px] font-medium hover:opacity-90 {!issues.length
					? 'text-side-dim hover:bg-side-hover'
					: TONE_PILL[worstTone]}"
			>
				{#if issues.length}
					<TriangleAlert size={16} />
					{issues.length} issue{issues.length === 1 ? '' : 's'}
				{:else}
					<CircleCheck size={16} class="text-ok" /> No issues
				{/if}
			</button>
		{/snippet}
		{#snippet children({ close })}
			<div class="w-[30rem] max-w-[90vw] text-xs">
				<div class="flex items-center justify-between gap-3 border-b border-line px-3 py-2.5">
					<span class="flex items-center gap-1.5 text-sm font-semibold text-ink"
						>Issues{#if issues.length}<span
								class="rounded-full px-1.5 text-[11px] leading-4 font-semibold text-white {worstTone ===
								'danger'
									? 'bg-danger'
									: 'bg-warn-ink'}">{issues.length}</span
							>{/if}</span
					>
					<span class="text-ink-faint">GitOps state and cluster alerts, one list</span>
				</div>
				{#if !issues.length}
					<p class="flex items-center gap-2 px-3 py-2.5 text-ink-faint">
						<CircleCheck size={14} class="text-ok" /> No standing issues.
					</p>
				{:else}
					<ul class="max-h-96 divide-y divide-line-soft overflow-y-auto">
						{#each issues.slice(0, 12) as i (i.scope + i.label)}
							<IssueRow issue={i} onleave={close} />
						{/each}
					</ul>
					{#if issues.length > 12}
						<p class="border-t border-line-soft px-3 py-1.5 text-right text-[11px] text-ink-faint">
							and {issues.length - 12} more
						</p>
					{/if}
				{/if}
				{#if suggestions.length}
					<div
						class="border-t border-line bg-inset px-3 pt-2.5 pb-0.5 text-[11px] font-semibold tracking-wide text-ink-faint uppercase"
					>
						Suggestions
					</div>
					<ul class="max-h-48 divide-y divide-line-soft overflow-y-auto bg-inset">
						{#each suggestions.slice(0, 5) as i (i.scope + i.label)}
							<IssueRow issue={i} onleave={close} />
						{/each}
					</ul>
				{/if}
				{#if inventory.alerts === null}
					<p class="border-t border-line-soft px-3 py-1.5 text-ink-faint">
						Cluster alerts unavailable: metrics are off or unreachable.
					</p>
				{/if}
			</div>
		{/snippet}
	</HeaderMenu>

	<HeaderMenu align="right">
		{#snippet trigger({ open, toggle })}
			<button
				onclick={toggle}
				aria-expanded={open}
				class="flex h-8 items-center gap-2 rounded-md px-1 text-[13px] text-side-ink hover:bg-side-hover"
			>
				<span class="flex size-7 items-center justify-center rounded-full bg-side-active">
					<UserIcon size={16} />
				</span>
				{session.user?.username}
				<ChevronDown size={16} class="transition-transform {open ? 'rotate-180' : ''}" />
			</button>
		{/snippet}
		{#snippet children({ close })}
			<div class="border-b border-line-soft px-3 py-2">
				<div class="font-medium text-ink">{session.user?.username}</div>
				{#if session.user?.groups.length}
					<div class="mt-0.5 text-[11px] break-words text-ink-faint">
						{session.user.groups.join(', ')}
					</div>
				{/if}
			</div>
			<div class="px-3 py-1.5 text-ink-muted">{inventory.vmCount} VMs in view</div>
			<div class="border-t border-line-soft"></div>
			<div class="px-3 pt-1.5 pb-0.5 text-[10px] tracking-wide text-ink-faint uppercase">Theme</div>
			{#each [{ id: 'light', label: 'Light' }, { id: 'dark', label: 'Dark' }, { id: 'system', label: 'System' }] as const as opt (opt.id)}
				<MenuItem onclick={() => (theme.mode = opt.id)}>
					{#snippet icon()}
						{#if opt.id === 'light'}<Sun size={13} />{:else if opt.id === 'dark'}<Moon
								size={13}
							/>{:else}<Monitor size={13} />{/if}
					{/snippet}
					{opt.label}
					{#if theme.mode === opt.id}<Check size={13} class="ml-auto text-accent-ink" />{/if}
				</MenuItem>
			{/each}
			<div class="border-t border-line-soft"></div>
			<MenuItem
				onclick={() => {
					close();
					session.logout();
				}}
			>
				Sign out
			</MenuItem>
		{/snippet}
	</HeaderMenu>
</header>
