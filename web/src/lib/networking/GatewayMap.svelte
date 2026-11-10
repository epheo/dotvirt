<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import type { Gateways } from '#lib/api.js';
	import {
		backendLabel,
		gatewayKey,
		layoutMap,
		listenersOf,
		NODE_H,
		NODE_W,
		parentOn,
		routeKey,
		routesOn,
		routeState,
		shares,
		urlFor,
	} from '#lib/gateways.js';
	import { plural } from '#lib/format.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import Note from '#lib/components/Note.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';

	// Every Gateway: what arrives on which port, which route takes it, and
	// where it goes. The view is the URL, so a shared link opens the same route.
	let { overview }: { overview: Gateways } = $props();

	// Above this a map stops being readable, and the table says the same.
	const MAP_ROUTES = 60;

	const current = $derived(
		overview.gateways.find((g) => gatewayKey(g) === page.url.searchParams.get('gateway')) ??
			overview.gateways[0],
	);
	const on = $derived(current ? routesOn(overview.routes, current) : []);
	const layout = $derived(
		current && on.length > 0 && on.length <= MAP_ROUTES
			? layoutMap(current, overview.routes)
			: null,
	);
	const selected = $derived(
		on.find((r) => routeKey(r) === page.url.searchParams.get('route')) ?? on[0],
	);
	const selectedKey = $derived(selected ? routeKey(selected) : '');
	const parent = $derived(selected && current ? parentOn(selected, current) : undefined);
	const state = $derived(routeState(parent));
	const arrives = $derived(selected && current ? listenersOf(selected, current) : []);
	const limits = $derived(
		current?.rateLimits?.filter((l) => !l.listener || arrives.some((a) => a.name === l.listener)) ??
			[],
	);
	const projects = $derived(
		new Set(on.map((r) => inventory.projectOf(r.namespace) || r.namespace)),
	);

	function open(params: Record<string, string>) {
		const q = new URLSearchParams(page.url.search);
		for (const [k, v] of Object.entries(params)) q.set(k, v);
		goto(`?${q}`, { replace: true, reset: false });
	}
	const pickGateway = (key: string) => {
		const q = new URLSearchParams(page.url.search);
		q.set('gateway', key);
		q.delete('route');
		goto(`?${q}`, { replace: true, reset: false });
	};

	const NODE = 'absolute flex flex-col justify-center rounded-md border px-3 text-left';
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5';
</script>

{#if !current}
	<p class="text-sm text-ink-muted">
		No Gateway yet. Your platform team declares one in the platform repo.
	</p>
{:else}
	{#if overview.gateways.length > 1}
		<div class="flex flex-wrap gap-2" role="tablist" aria-label="Gateways">
			{#each overview.gateways as g (gatewayKey(g))}
				<button
					type="button"
					role="tab"
					aria-selected={g === current}
					onclick={() => pickGateway(gatewayKey(g))}
					class="cursor-pointer rounded-md border px-3 py-1.5 text-[13px] {g === current
						? 'border-accent bg-select-soft font-bold text-accent-ink'
						: 'border-line-strong bg-panel text-ink-soft hover:bg-inset'}"
				>
					{g.name}
				</button>
			{/each}
		</div>
	{/if}

	<div class="flex flex-wrap items-center gap-x-5 gap-y-1 text-[13px]">
		<h2 class="text-lg font-bold text-ink">{current.name}</h2>
		<StatusPill
			tone={current.ready ? 'ok' : 'danger'}
			label={current.ready ? 'Serving' : (current.reason ?? 'Not serving')}
		/>
		{#if current.addresses?.length}
			<span class="font-mono text-ink">{current.addresses.join(', ')}</span>
		{/if}
		<span class="text-ink-faint">class {current.class}</span>
		<span class="text-ink-faint">
			{plural(on.length, 'route')} from {plural(projects.size, 'project')}
		</span>
	</div>
	{#if !current.ready && current.message}
		<Note tone="danger" class="px-3 py-2 text-xs">{current.message}</Note>
	{/if}

	<div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
		<Card title="What comes in, and where it goes" flush>
			{#if layout}
				<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
				<div class="overflow-x-auto p-4" role="region" aria-label="Route map" tabindex="0">
					<div class="relative" style:width="{layout.width}px" style:height="{layout.height}px">
						<svg
							class="absolute inset-0"
							width={layout.width}
							height={layout.height}
							fill="none"
							aria-hidden="true"
						>
							{#each layout.links as l, i (i)}
								<path
									d={l.d}
									stroke-width={l.route === selectedKey ? 2.5 : 1.5}
									stroke-dasharray={l.live ? undefined : '5 4'}
									class={l.live
										? l.route === selectedKey
											? 'stroke-accent'
											: 'stroke-line-strong'
										: 'stroke-danger'}
								/>
							{/each}
						</svg>
						{#each layout.listeners as n (n.key)}
							<div
								class="{NODE} border-line bg-inset"
								style:left="{n.x}px"
								style:top="{n.y}px"
								style:width="{NODE_W}px"
								style:height="{NODE_H}px"
							>
								<span class="font-mono text-xs font-medium text-ink">
									:{n.listener.port}
									{n.listener.protocol}
								</span>
								<span class="truncate text-[11px] text-ink-faint">
									{n.listener.hostname || n.listener.name}
								</span>
							</div>
						{/each}
						{#each layout.routes as n (n.key)}
							<button
								type="button"
								aria-pressed={n.key === selectedKey}
								onclick={() => open({ route: n.key })}
								class="{NODE} cursor-pointer {n.key === selectedKey
									? 'border-accent bg-select'
									: n.state.live
										? 'border-line bg-panel hover:bg-inset'
										: 'border-danger/40 bg-danger-soft'}"
								style:left="{n.x}px"
								style:top="{n.y}px"
								style:width="{NODE_W}px"
								style:height="{NODE_H}px"
							>
								<span class="truncate font-mono text-xs font-medium text-ink">
									{n.route.hostnames?.[0] ?? n.route.name}
								</span>
								<span
									class="truncate text-[11px] {n.state.live ? 'text-ink-faint' : 'text-danger-ink'}"
								>
									{n.state.live ? n.route.namespace : n.state.label}
								</span>
							</button>
						{/each}
						{#each layout.backends as n (n.key)}
							<div
								class="{NODE} border-line bg-panel"
								style:left="{n.x}px"
								style:top="{n.y}px"
								style:width="{NODE_W}px"
								style:height="{NODE_H}px"
							>
								<span class="truncate font-mono text-xs font-medium text-ink">
									{backendLabel(n.backend)}
								</span>
								<span class="truncate text-[11px] text-ink-faint">{n.backend.namespace}</span>
							</div>
						{/each}
					</div>
				</div>
			{:else}
				<p class="p-4 text-[13px] text-ink-muted">
					{on.length
						? `${on.length} routes: see the list below.`
						: 'No route uses this Gateway yet.'}
				</p>
			{/if}
		</Card>

		{#if selected}
			<aside
				aria-label="Selected route"
				class="flex min-w-0 flex-col gap-3 rounded-lg border border-line bg-panel p-4"
			>
				<div class="flex flex-col gap-1">
					<h3 class="font-mono text-sm font-medium text-ink">{selected.name}</h3>
					<span class="text-xs text-ink-faint">
						{selected.kind}, {inventory.projectOf(selected.namespace) || selected.namespace}
					</span>
				</div>
				<div class="flex flex-col items-start gap-1">
					<StatusPill tone={state.tone} label={state.label} />
					<p class="text-[13px] text-ink-muted">{state.detail}</p>
					{#if parent?.message && !state.live}
						<Note tone="danger" class="px-3 py-2 text-xs">{parent.message}</Note>
					{/if}
				</div>
				{#if selected.hostnames?.length}
					<div class="flex flex-col gap-1">
						<h4 class="text-xs font-bold text-ink-muted">
							{state.live ? 'Reachable at' : 'Asks for'}
						</h4>
						{#each selected.hostnames as host (host)}
							{#if state.live}
								<a
									class="font-mono text-[13px] text-accent-ink hover:underline"
									href={urlFor(selected, host, arrives[0])}
									target="_blank"
									rel="noreferrer"
								>
									{host}
								</a>
							{:else}
								<span class="font-mono text-[13px] text-ink">{host}</span>
							{/if}
						{/each}
					</div>
				{/if}
				{#if arrives.length}
					<div class="flex flex-col gap-1">
						<h4 class="text-xs font-bold text-ink-muted">Arrives on</h4>
						<span class="font-mono text-[13px] text-ink">
							{arrives.map((a) => `:${a.port} ${a.protocol}`).join(', ')}
						</span>
					</div>
				{/if}
				{#if limits.length}
					<div class="flex flex-col gap-1">
						<h4 class="text-xs font-bold text-ink-muted">Rate limit, per client</h4>
						{#each limits as l, i (i)}
							<span class="text-[13px] text-ink">
								{l.requestsPerSecond} requests a second, bursts of {l.burst}{l.listener
									? ` on ${l.listener}`
									: ''}
							</span>
						{/each}
					</div>
				{/if}
				{#if selected.backends?.length}
					<div class="flex flex-col gap-1">
						<h4 class="text-xs font-bold text-ink-muted">Sends to</h4>
						{#each shares(selected) as s, i (i)}
							<div class="flex items-center justify-between gap-2 text-[13px]">
								<span class="truncate font-mono text-ink">{backendLabel(s.backend)}</span>
								{#if (selected.backends?.length ?? 0) > 1}
									<span class="text-ink-faint">{s.percent}%</span>
								{/if}
							</div>
						{/each}
					</div>
				{/if}
			</aside>
		{/if}
	</div>

	{#if on.length}
		<Card title="Routes" meta={plural(on.length, 'route')} flush>
			<div class="overflow-x-auto">
				<table class="w-full text-[13px]">
					<thead>
						<tr>
							<th scope="col" class={TH}>Route</th>
							<th scope="col" class={TH}>Project</th>
							<th scope="col" class={TH}>Sends to</th>
							<th scope="col" class={TH}>State</th>
						</tr>
					</thead>
					<tbody>
						{#each on as r (routeKey(r))}
							{@const s = routeState(parentOn(r, current))}
							<tr class={routeKey(r) === selectedKey ? 'bg-select-soft' : ''}>
								<td class={TD}>
									<button
										type="button"
										class="cursor-pointer text-left font-mono text-accent-ink hover:underline"
										onclick={() => open({ route: routeKey(r) })}
									>
										{r.hostnames?.join(', ') || r.name}
									</button>
								</td>
								<td class="{TD} text-ink-muted"
									>{inventory.projectOf(r.namespace) || r.namespace}</td
								>
								<td class="{TD} font-mono text-xs text-ink-muted">
									{(r.backends ?? []).map(backendLabel).join(', ')}
								</td>
								<td class={TD}><StatusPill tone={s.tone} label={s.label} /></td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</Card>
	{/if}
{/if}
