<script lang="ts">
	import { Activity, GitBranch, Map as MapIcon } from '@lucide/svelte';
	import { page } from '$app/state';
	import { api, type NetworkRules } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { POD_NETWORK } from '#lib/lenses.js';
	import { cutOffHosts } from '#lib/netmap.js';
	import { NETWORK_TABS } from '#lib/nav.js';
	import { networkByRef, networkKey, networkKind } from '#lib/networks.js';
	import { canAuthorNetwork, networkRef } from '#lib/objects.js';
	import { resource } from '#lib/resource.svelte.js';
	import { hasOwnRule, networkRuleLines } from '#lib/rulelines.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import FileHistory from '#lib/components/FileHistory.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import TabBar from '#lib/components/TabBar.svelte';
	import NetworkSummary from './NetworkSummary.svelte';
	import NetworkVMs from './NetworkVMs.svelte';
	import RuleList from './RuleList.svelte';

	// One network's page: where it connects, how full it is and what can
	// change on Summary; its VMs, the rules on its traffic and its file's
	// history on their own tabs. key is the route's network key.
	let { key }: { key: string } = $props();

	const isPod = $derived(key === POD_NETWORK);
	const net = $derived(networking.byKey.get(key) ?? networkByRef(key, inventory.networks) ?? null);
	const nkey = $derived(net ? networkKey(net) : key);
	const tab = $derived.by(() => {
		const t = page.url.searchParams.get('tab');
		return NETWORK_TABS.some((x) => x.id === t) ? t! : 'summary';
	});
	const vms = $derived(networking.vmsOn(nkey));
	const owner = $derived(net ? networking.ownerOf(net) : '');
	const project = $derived(net?.scope === 'shared' ? 'platform' : owner);

	// Rules ride the catalog's version: a merged rule change moves it.
	const rulesRes = resource<NetworkRules | null>(
		() => (net ? `${nkey}|${inventory.networksVersion}` : ''),
		(k) => (k ? api.networkRules(k.slice(0, k.lastIndexOf('|'))) : Promise.resolve(null)),
	);
	const lines = $derived(rulesRes.data ? networkRuleLines(rulesRes.data) : []);
	const podLines = $derived(
		isPod
			? networkRuleLines({
					network: key,
					namespaces: [],
					applies: networking.clusterRules.map((policy) => ({ policy })),
					reaches: [],
				})
			: [],
	);
	const ruleLines = $derived(isPod ? podLines : lines);

	const counts: Record<string, number | undefined> = $derived({
		vms: vms.length,
		rules: isPod || rulesRes.data ? ruleLines.length : undefined,
	});
	const tabs = $derived(NETWORK_TABS.map((t) => ({ ...t, count: counts[t.id] })));

	const problems = $derived(networking.problemsOf(nkey));
	const cut = $derived(net ? cutOffHosts(net, inventory.uplinks) : []);
	const kindCrumb = $derived(
		isPod
			? null
			: net
				? {
						label: `${networkKind(net)}s`,
						href: `/networking?tab=networks&kind=${net.kind === 'vlan' ? 'vlan' : net.scope === 'shared' ? 'shared' : 'project'}`,
					}
				: null,
	);
	const trail = $derived([
		{ label: 'Networking', href: '/networking' },
		...(kindCrumb ? [kindCrumb] : []),
		...(owner
			? [{ label: owner, href: `/networking?tab=networks&project=${encodeURIComponent(owner)}` }]
			: []),
		{ label: net?.name ?? key },
	]);
	const tone = $derived(cut.length ? 'danger' : problems.length ? 'warn' : 'ok');
	const mapHref = $derived(
		`/networking?find=${encodeURIComponent(net?.name ?? '')}&sel=${encodeURIComponent(`net:${nkey}`)}`,
	);
</script>

{#snippet status()}
	{#if net?.kind === 'vlan'}
		VLAN{net.vlan ? ` ${net.vlan}` : ''} on {net.uplink ?? 'an uplink'}{#if cut.length}, cut off on {cut.join(
				', ',
			)}{/if}
	{:else if net?.scope === 'shared'}
		{@const n = networking.projectsOf(net).length}
		Shared network, {n} project{n === 1 ? '' : 's'}
	{:else if net}
		Project network of <a
			href="/networking?tab=networks&project={encodeURIComponent(owner)}"
			class="text-accent-ink underline underline-offset-2">{owner || net.namespace}</a
		>
	{/if}
{/snippet}

{#snippet context()}
	<span class="flex items-center gap-4 text-[13px] text-ink-faint">
		{#if net?.subnets?.length}<span class="font-mono">{net.subnets.join(', ')}</span>{/if}
		{#if net}
			<span class="inline-flex items-center gap-1.5 text-ink-soft">
				<GitBranch size={14} class="text-accent-ink" />{net.sourceFile ? 'In git' : 'Not in git'}
			</span>
		{/if}
	</span>
{/snippet}

<div class="flex min-h-0 flex-1 flex-col bg-canvas">
	<div class="flex flex-col gap-2 px-6 pt-5">
		<Breadcrumb {trail} />
		{#if isPod}
			<PageHeader
				title="Pod network"
				status={{ tone: 'ok', text: 'Built in, every project' }}
				context="{plural(networking.members.podOnly, 'VM')} {networking.members.podOnly === 1
					? 'uses'
					: 'use'} only this"
			>
				{#snippet actions()}
					<Button variant="secondary" href="/networking?sel=pod"
						><MapIcon size={16} />Show on map</Button
					>
					<Button variant="outline" onclick={() => (ui.modal = { kind: 'trace' })}
						><Activity size={16} />Check connectivity</Button
					>
				{/snippet}
			</PageHeader>
		{:else if net}
			<PageHeader title={net.name} status={{ tone, text: status }} {context}>
				{#snippet actions()}
					<Button variant="secondary" href={mapHref}><MapIcon size={16} />Show on map</Button>
					<Button variant="outline" onclick={() => (ui.modal = { kind: 'trace' })}
						><Activity size={16} />Check connectivity</Button
					>
				{/snippet}
			</PageHeader>
		{:else}
			<PageHeader title={key} status={{ tone: 'off', text: 'Not found' }} />
		{/if}
		<TabBar {tabs} active={tab} href={(t) => `?tab=${t}`} />
	</div>

	{#if !net && !isPod}
		<div class="px-6 pt-4">
			<Card title="No such network">
				<p class="text-[13px] text-ink-soft">
					No network named {key} is visible to you. It may have been deleted, or it lives in a project
					you cannot see.
				</p>
				<div class="mt-3">
					<Button variant="secondary" href="/networking?tab=networks">All networks</Button>
				</div>
			</Card>
		</div>
	{:else if tab === 'vms'}
		<NetworkVMs {vms} />
	{:else}
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div
			class="min-h-0 flex-1 overflow-y-auto px-6 pt-4 pb-7"
			role="region"
			aria-label="{net?.name ?? key}, {tab}"
			tabindex="0"
		>
			{#if tab === 'summary'}
				<NetworkSummary
					{net}
					{nkey}
					{vms}
					{project}
					rules={rulesRes.data}
					lines={ruleLines}
					rulesFailed={rulesRes.failed}
				/>
			{:else if tab === 'rules'}
				{#if rulesRes.failed}
					<Card title="Rules that apply">
						<p class="text-[13px] text-danger-ink">The rules could not be read.</p>
					</Card>
				{:else if !isPod && !rulesRes.data}
					<Card title="Rules that apply">
						<p class="text-[13px] text-ink-faint">Reading the rules...</p>
					</Card>
				{:else}
					{@const groups = [
						{ id: 'own', title: 'Its own rules', meta: 'binding its namespace or its VMs' },
						{
							id: 'names',
							title: 'Rules elsewhere that name it',
							meta: 'naming its namespace or project',
						},
						{
							id: 'wildcard',
							title: 'Rules elsewhere that let any namespace in',
							meta: 'matching it without naming it',
						},
					]}
					<div class="flex flex-col gap-4">
						{#each groups as g (g.id)}
							{@const list = ruleLines.filter((l) => l.group === g.id)}
							{#if g.id === 'own' || list.length}
								<Card title={g.title} meta="{list.length}, {g.meta}">
									{#if g.id === 'own' && !isPod && !hasOwnRule(ruleLines)}
										<p class="mb-3 text-[13px] text-ink-soft">
											No rule of its own: every VM here accepts traffic from anywhere the cluster
											rules allow.
										</p>
									{/if}
									{#if list.length}
										<RuleList lines={list} owner gap="gap-3" />
									{/if}
								</Card>
							{/if}
						{/each}
					</div>
				{/if}
			{:else if net?.sourceFile && project}
				{@const ref = networkRef(net)}
				<FileHistory
					{project}
					name={net.name}
					load={() => api.objectHistory(ref.resource, ref.namespace, ref.name)}
					restore={canAuthorNetwork(net)
						? (hash) => api.restoreObjectVersion(ref.resource, ref.namespace, ref.name, hash)
						: undefined}
					mine={(it) => it.resource === ref.resource && it.name === ref.name}
				/>
			{:else}
				<Card title="History">
					<p class="text-[13px] text-ink-faint">
						{isPod
							? 'The pod network is built in: git holds nothing for it.'
							: 'Not in git, so it has no history here.'}
					</p>
				</Card>
			{/if}
		</div>
	{/if}
</div>
