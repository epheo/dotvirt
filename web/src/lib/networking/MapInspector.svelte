<script lang="ts">
	import { Activity, X } from '@lucide/svelte';
	import {
		api,
		vmKey,
		type EffectivePolicy,
		type Network,
		type Uplink,
		type VM,
	} from '#lib/api.js';
	import { quantity } from '#lib/format.js';
	import type { NetProblem } from '#lib/netmap.js';
	import { uplinkLabel, wayOut } from '#lib/netmap.js';
	import { networkHref, vmHref } from '#lib/nav.js';
	import { networkKey, networkKind } from '#lib/networks.js';
	import { canEditUplink, openEdit, uplinkRef } from '#lib/objects.js';
	import { resource } from '#lib/resource.svelte.js';
	import { hasOwnRule, topLines, vmRuleLines } from '#lib/rulelines.js';
	import { vmSizing } from '#lib/sizing.js';
	import { vmState } from '#lib/status.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { vmsOff } from '#lib/state/modules.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import RuleList from './RuleList.svelte';

	// The map's inspector: what the selected thing is, how it leaves the
	// cluster, the rules that apply as sentences, and the next action.
	type Target =
		| { kind: 'vm'; vm: VM }
		| { kind: 'network'; net: Network }
		| { kind: 'uplink'; uplink: Uplink; problem: NetProblem | null }
		| { kind: 'project'; name: string }
		| { kind: 'edge' }
		| { kind: 'fabric' }
		| { kind: 'pod' };
	let { target, onclose }: { target: Target; onclose: () => void } = $props();

	const vm = $derived(target.kind === 'vm' ? target.vm : null);
	// Keyed on the VM's identity: the live stream hands a fresh object per frame.
	const key = $derived(vm ? vmKey(vm) : '');
	const effRes = resource<EffectivePolicy | null>(
		() => key,
		(k) => {
			if (!k) return Promise.resolve(null);
			const [ns, name] = k.split('/');
			return api.vmPolicy(ns, name);
		},
		{ reset: true },
	);
	const eff = $derived(effRes.data);
	const project = $derived(vm ? inventory.projectOf(vm.namespace) : '');
	const way = $derived(
		vm
			? wayOut(vm, inventory.networks, inventory.uplinks, {
					snat: eff?.snat?.find((b) => !b.conditional)?.policy,
					route: eff?.routes?.find((b) => !b.conditional)?.policy,
				})
			: null,
	);
	const lines = $derived(eff ? vmRuleLines(eff, project) : []);
	const size = $derived.by(() => {
		if (!vm) return '';
		const s = vmSizing(vm, inventory.options);
		return [s.cpu ? `${s.cpu} vCPU` : '', s.memory ? quantity(s.memory) : '']
			.filter(Boolean)
			.join(', ');
	});
	const config = (v: VM) =>
		v.sync === 'Synced'
			? 'In git, matches the cluster'
			: v.sync === 'OutOfSync'
				? 'In git, differs from the cluster'
				: v.sync === 'NotTracked'
					? 'Not in git'
					: v.sync === 'Pending'
						? 'In git, waiting for its first sync'
						: 'In git';

	const title = $derived(
		target.kind === 'vm'
			? target.vm.name
			: target.kind === 'network'
				? target.net.name
				: target.kind === 'uplink'
					? uplinkLabel(target.uplink)
					: target.kind === 'project'
						? target.name
						: target.kind === 'edge'
							? 'Cluster edge'
							: target.kind === 'fabric'
								? 'Datacenter fabric'
								: 'Pod network',
	);
	const meta = $derived(
		target.kind === 'vm'
			? 'VM'
			: target.kind === 'network'
				? networkKind(target.net)
				: target.kind === 'uplink'
					? 'Uplink'
					: target.kind === 'project'
						? 'Project'
						: '',
	);
	const mono = $derived(['vm', 'network', 'uplink', 'project'].includes(target.kind));
	const fmt = (n: number) => n.toLocaleString('en-US');
	// VM counts read 0 without KubeVirt, so they drop rather than mislead.
	const vmRow = (n: number): [string, string][] => (vmsOff() ? [] : [['VMs', fmt(n)]]);
</script>

{#snippet head()}
	<button
		type="button"
		onclick={onclose}
		aria-label="Close inspector"
		class="inline-flex size-7 items-center justify-center rounded-md text-ink-soft hover:bg-inset-strong"
		><X size={16} /></button
	>
{/snippet}

{#snippet kv(rows: [string, string, boolean?][])}
	<dl class="grid grid-cols-[76px_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[13px]">
		{#each rows as [k, v, isMono] (k)}
			<dt class="text-ink-faint">{k}</dt>
			<dd class="min-w-0 {isMono ? 'font-mono font-medium' : ''}">{v}</dd>
		{/each}
	</dl>
{/snippet}

{#snippet sh(text: string, aside?: string, link?: { label: string; href: string })}
	<h3 class="mb-2 flex items-baseline gap-2 text-[13px] leading-5 font-bold">
		{text}
		{#if aside}<span class="text-[13px] font-normal text-ink-faint">{aside}</span>{/if}
		{#if link}<a href={link.href} class="ml-auto font-normal text-accent-ink hover:underline"
				>{link.label}</a
			>{/if}
	</h3>
{/snippet}

<aside class="w-[320px] shrink-0" aria-label="Inspector, {title}">
	<div class="[&_h2]:text-[15px] {mono ? '[&_h2]:font-mono [&_h2]:font-medium' : ''}">
		<Card {title} {meta} actions={head}>
			<div class="flex flex-col gap-[18px]">
				{#if vm && way}
					{@const st = vmState(vm)}
					<div class="flex flex-col gap-0.5">
						<span class="inline-flex items-center gap-1.5 text-[13px] text-ink-soft">
							<span
								class="size-2 rounded-full {st.tone === 'ok'
									? 'bg-ok'
									: st.tone === 'danger'
										? 'bg-danger'
										: st.tone === 'warn'
											? 'bg-warn'
											: 'bg-ink-faint'}"
							></span>
							{st.word}{vm.nodeName ? ` on ${vm.nodeName}` : ''}
						</span>
						<span class="text-[13px] text-ink-faint">In {project}, namespace {vm.namespace}</span>
					</div>
					{@render kv([
						['Network', way.steps[0].text],
						['Address', vm.guestIP ?? 'None reported', !!vm.guestIP],
						['Size', size || 'Not set'],
						['Config', config(vm)],
					])}
					<div>
						{@render sh('Way out', way.via === 'none' ? undefined : 'blue on the map')}
						<ol class="flex flex-col gap-1.5 text-[13px] text-ink-soft">
							{#each way.steps as s, i (i)}
								<li class="flex items-start gap-2">
									<span
										class="mt-px size-[18px] shrink-0 rounded-full bg-accent text-center text-[11px] leading-[18px] font-bold text-accent-on"
										>{i + 1}</span
									>
									<span>{s.text}</span>
								</li>
							{/each}
						</ol>
						{#if way.also.length}
							<p class="mt-2 text-xs text-ink-faint">
								Also on {way.also.map((n) => n.name).join(', ')}, straight to the datacenter fabric.
							</p>
						{/if}
					</div>
					<div>
						{@render sh('Rules that apply', undefined, {
							label: 'Firewall tab',
							href: `/networking?tab=firewall&tenant=${encodeURIComponent(project)}`,
						})}
						{#if effRes.failed}
							<p class="text-xs text-danger-ink">The rules could not be read.</p>
						{:else if !eff}
							<p class="text-xs text-ink-faint">Reading the rules...</p>
						{:else}
							{#if !hasOwnRule(lines)}
								<p class="mb-2.5 text-[13px] text-ink-soft">
									No rule of its own: it accepts traffic from anywhere the cluster rules allow.
								</p>
							{/if}
							<RuleList lines={topLines(lines, 3)} />
							{#if lines.length > 3}
								<p class="mt-2 text-xs text-ink-faint">
									{lines.length - 3} more on the Firewall tab.
								</p>
							{/if}
						{/if}
					</div>
					<div class="flex flex-col gap-2 border-t border-line-soft pt-4">
						<div class="flex flex-wrap gap-2">
							<Button
								class="px-3!"
								onclick={() =>
									(ui.modal = {
										kind: 'trace',
										source: { namespace: vm.namespace, name: vm.name },
									})}><Activity size={16} />Check connectivity</Button
							>
							<Button variant="secondary" class="px-3!" href={vmHref(vm.namespace, vm.name)}
								>Open {vm.name}</Button
							>
						</div>
						<span class="text-xs leading-4 text-ink-faint"
							>Check connectivity simulates the rules. It sends no packet.</span
						>
					</div>
				{:else if target.kind === 'network'}
					{@const n = target.net}
					{@const k = networking.vmsOn(networkKey(n))}
					{@render kv([
						['Subnet', n.subnets?.join(', ') || 'None', !!n.subnets?.length],
						...vmRow(k.length),
						...(n.kind === 'vlan'
							? ([['VLAN', `${n.vlan ?? 'untagged'} on ${n.uplink ?? 'an uplink'}`]] as [
									string,
									string,
								][])
							: []),
						[
							'Projects',
							n.scope === 'shared' ? fmt(n.namespaces?.length ?? 0) : networking.ownerOf(n),
						],
						['Config', n.sourceFile ? 'In git' : 'Not in git'],
					])}
					<div class="flex flex-wrap gap-2 border-t border-line-soft pt-4">
						<Button href={networkHref(networkKey(n))}>Open {n.name}</Button>
					</div>
				{:else if target.kind === 'uplink'}
					{@const u = target.uplink}
					{@render kv([
						['Bridge', u.bridge || 'Unknown', !!u.bridge],
						['Hosts', fmt(u.nodeCount)],
						['Policy', u.policy ?? (u.builtin ? 'Built in' : 'Several'), !!u.policy],
					])}
					{#if target.problem}
						<div class="flex flex-col gap-1.5">
							{@render sh('Problem')}
							<p class="text-[13px] text-danger-ink">{target.problem.detail}</p>
							{#each u.failures ?? [] as f (f.node)}
								{#if f.message}<p class="font-mono text-[11px] text-ink-faint">
										{f.node}: {f.message}
									</p>{/if}
							{/each}
							{#if canEditUplink(u)}
								<div class="mt-1 flex flex-wrap items-center gap-1.5">
									<Button variant="outline" size="sm" onclick={() => openEdit(uplinkRef(u))}
										>Edit {uplinkLabel(u)}</Button
									>
									<StageChip kind="pr" />
								</div>
							{/if}
						</div>
					{/if}
					<div class="border-t border-line-soft pt-4">
						<Button variant="secondary" href="/networking?tab=uplinks">Open the Uplinks tab</Button>
					</div>
				{:else if target.kind === 'project'}
					{@const p = inventory.projects.find((x) => x.name === target.name)}
					{@const nets = p ? networking.netsOfProject(p) : []}
					{@render kv([
						...vmRow(p?.namespaces.reduce((s, n) => s + n.vms.length, 0) ?? 0),
						['Networks', fmt(nets.length)],
					])}
					{#if nets.length}
						<ul class="flex flex-col gap-1 text-[13px]">
							{#each nets.slice(0, 6) as n (networkKey(n))}
								<li>
									<a
										href={networkHref(networkKey(n))}
										class="font-mono text-accent-ink underline underline-offset-2">{n.name}</a
									>
									<span class="text-ink-faint">{n.subnets?.[0] ?? ''}</span>
								</li>
							{/each}
						</ul>
					{/if}
					<div class="border-t border-line-soft pt-4">
						<Button
							variant="secondary"
							href="/networking?tab=networks&project={encodeURIComponent(target.name)}"
							>Its networks</Button
						>
					</div>
				{:else if target.kind === 'edge'}
					<p class="text-[13px] text-ink-soft">
						Where traffic leaves the cluster. The source address becomes the host's, unless a fixed
						outbound IP or an external route applies.
					</p>
					{@render kv([
						['Fixed IPs', fmt(networking.egress.filter((p) => p.kind === 'egressip').length)],
						['Routes', fmt(networking.egress.filter((p) => p.kind === 'route').length)],
					])}
					<div class="border-t border-line-soft pt-4">
						<Button variant="secondary" href="/networking?tab=firewall"
							>Open the Firewall tab</Button
						>
					</div>
				{:else if target.kind === 'fabric'}
					<p class="text-[13px] text-ink-soft">
						{vmsOff()
							? 'The physical switches. VLAN networks reach it through their uplink and skip the cluster edge.'
							: 'The physical switches. VLAN networks reach it through their uplink, skip the cluster edge, and VMs keep their own address.'}
					</p>
					{@render kv([['VLANs', fmt(networking.vlanNets.length)]])}
				{:else}
					<p class="text-[13px] text-ink-soft">
						Built in to every project. {vmsOff() ? 'Workloads' : 'VMs'} without another network leave
						through the default uplink and the cluster edge.
					</p>
					{#if !vmsOff()}{@render kv([['Only this', fmt(networking.members.podOnly)]])}{/if}
					<div class="border-t border-line-soft pt-4">
						<Button variant="secondary" href={networkHref('Pod network')}>Open Pod network</Button>
					</div>
				{/if}
			</div>
		</Card>
	</div>
</aside>
