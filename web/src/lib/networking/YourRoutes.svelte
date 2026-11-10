<script lang="ts">
	import { page } from '$app/state';
	import type { Route } from '#lib/api.js';
	import { backendLabel, routeKey, routeState } from '#lib/gateways.js';
	import Card from '#lib/components/Card.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';

	// A project's view: is each route live, and if not, what to do. The server's
	// rows carry a reason, never a controller's text, so nothing here can name
	// another tenant.
	let { routes }: { routes: Route[] } = $props();

	const rows = $derived(
		routes.map((r) => ({
			r,
			s: routeState(r.parents?.find((p) => !p.attached) ?? r.parents?.[0]),
		})),
	);
	const live = $derived(rows.filter((x) => x.s.live).length);
	// The route a landing tile sent the reader to.
	const focus = $derived(page.url.searchParams.get('route') ?? '');
</script>

{#if rows.length === 0}
	<p class="text-sm text-ink-muted">None of your projects publish a route yet.</p>
{:else}
	<Card title="Your routes" meta="{live} of {rows.length} live" flush>
		<ul>
			{#each rows as { r, s } (routeKey(r))}
				{@const focused = routeKey(r) === focus}
				<li
					class="flex flex-wrap items-center gap-x-5 gap-y-1.5 border-b border-line-soft px-card py-3 last:border-b-0 {focused
						? 'bg-select-soft'
						: ''}"
					aria-current={focused ? 'true' : undefined}
					{@attach focused && ((el) => el.scrollIntoView({ block: 'center' }))}
				>
					<div class="flex min-w-[260px] flex-1 flex-col">
						{#if r.hostnames?.length}
							{#each r.hostnames as host (host)}
								<span class="font-mono text-[13px] font-medium text-ink">{host}</span>
							{/each}
						{:else}
							<span class="font-mono text-[13px] font-medium text-ink">{r.name}</span>
						{/if}
						<span class="text-xs text-ink-faint">{r.kind}, {r.namespace}</span>
					</div>
					<span class="min-w-[160px] font-mono text-xs text-ink-muted">
						{(r.backends ?? []).map(backendLabel).join(', ')}
					</span>
					<div class="flex min-w-[260px] flex-[1.4] flex-col items-start gap-0.5">
						<StatusPill tone={s.tone} label={s.label} />
						<span class="text-[13px] text-ink-muted">{s.detail}</span>
					</div>
				</li>
			{/each}
		</ul>
	</Card>
{/if}
