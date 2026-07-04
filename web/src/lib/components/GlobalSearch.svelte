<script lang="ts">
	// vCenter's masthead search: one box over the whole streamed inventory — VMs
	// (name, IP, labels), projects, namespaces, nodes. Pure frontend: the inventory
	// is already client-side. `label:key=value` (or `label:key`) narrows to VM
	// labels — the tags-parity affordance; label chips elsewhere call searchFor().
	import { Search } from 'lucide-svelte';
	import type { Inventory, VM } from '$lib/api';

	export type SearchHit =
		| { kind: 'vm'; vm: VM; hint: string }
		| { kind: 'project'; project: string }
		| { kind: 'namespace'; project: string; namespace: string }
		| { kind: 'node'; node: string };

	let {
		inventory,
		onpick,
	}: {
		inventory: Inventory | null;
		onpick: (hit: SearchHit) => void;
	} = $props();

	let query = $state('');
	let open = $state(false);
	let active = $state(0);
	let input = $state<HTMLInputElement | null>(null);

	// Focus + prefill from outside (label chips → `label:k=v`).
	export function searchFor(q: string) {
		query = q;
		open = true;
		active = 0;
		input?.focus();
	}

	const hits = $derived.by((): SearchHit[] => {
		const q = query.trim().toLowerCase();
		if (!inventory || !q) return [];
		const out: SearchHit[] = [];

		// label:key=value / label:key — VM-label search only.
		const labelQ = q.startsWith('label:') ? q.slice('label:'.length) : null;

		const vms = inventory.projects.flatMap((p) => p.namespaces.flatMap((n) => n.vms));
		for (const vm of vms) {
			if (out.length >= 8) break;
			const labels = Object.entries(vm.labels ?? {});
			if (labelQ !== null) {
				const [k, v] = labelQ.split('=', 2);
				const m = labels.find(([lk, lv]) =>
					v === undefined
						? lk.toLowerCase().includes(k)
						: lk.toLowerCase() === k && lv.toLowerCase() === v,
				);
				if (m) out.push({ kind: 'vm', vm, hint: `${m[0]}=${m[1]}` });
				continue;
			}
			if (vm.name.toLowerCase().includes(q) || vm.namespace.toLowerCase().includes(q)) {
				out.push({ kind: 'vm', vm, hint: vm.namespace });
				continue;
			}
			const ips = vm.ips ?? (vm.guestIP ? [vm.guestIP] : []);
			const ip = ips.find((i) => i.includes(q));
			if (ip) {
				out.push({ kind: 'vm', vm, hint: ip });
				continue;
			}
			const lab = labels.find(([k, v]) => `${k}=${v}`.toLowerCase().includes(q));
			if (lab) out.push({ kind: 'vm', vm, hint: `${lab[0]}=${lab[1]}` });
		}

		if (labelQ === null) {
			for (const p of inventory.projects) {
				if (p.name.toLowerCase().includes(q)) out.push({ kind: 'project', project: p.name });
				for (const n of p.namespaces) {
					if (n.namespace.toLowerCase().includes(q))
						out.push({ kind: 'namespace', project: p.name, namespace: n.namespace });
				}
			}
			const nodes = [...new Set(vms.map((v) => v.nodeName).filter(Boolean))] as string[];
			for (const node of nodes) {
				if (node.toLowerCase().includes(q)) out.push({ kind: 'node', node });
			}
		}
		return out.slice(0, 14);
	});

	// Clamp the keyboard cursor when the hit list shrinks under it.
	$effect(() => {
		if (active >= hits.length) active = 0;
	});

	function pick(hit: SearchHit) {
		onpick(hit);
		query = '';
		open = false;
		input?.blur();
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			active = Math.min(active + 1, hits.length - 1);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			active = Math.max(active - 1, 0);
		} else if (e.key === 'Enter' && hits[active]) {
			e.preventDefault();
			pick(hits[active]);
		} else if (e.key === 'Escape') {
			open = false;
			input?.blur();
		}
	}

	// Global shortcuts: Ctrl/Cmd-K always; "/" when not already typing somewhere.
	function onWindowKey(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
			e.preventDefault();
			input?.focus();
			open = true;
		} else if (e.key === '/' && !isEditable(e.target)) {
			e.preventDefault();
			input?.focus();
			open = true;
		}
	}

	function isEditable(t: EventTarget | null): boolean {
		const el = t as HTMLElement | null;
		return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable);
	}

	function hitLabel(h: SearchHit): string {
		switch (h.kind) {
			case 'vm':
				return h.vm.name;
			case 'project':
				return h.project;
			case 'namespace':
				return h.namespace;
			case 'node':
				return h.node;
		}
	}
	function hitHint(h: SearchHit): string {
		switch (h.kind) {
			case 'vm':
				return h.hint;
			case 'namespace':
				return h.project;
			default:
				return '';
		}
	}
	const kindBadge: Record<SearchHit['kind'], string> = {
		vm: 'VM',
		project: 'Project',
		namespace: 'Namespace',
		node: 'Node',
	};
</script>

<svelte:window onkeydown={onWindowKey} />

<div class="relative mx-auto w-80">
	<div class="flex items-center gap-2 rounded bg-slate-700 px-2.5 py-1">
		<Search size={13} class="shrink-0 text-slate-400" />
		<input
			bind:this={input}
			bind:value={query}
			onfocus={() => (open = true)}
			{onkeydown}
			placeholder="Search VMs, projects, nodes, label:k=v"
			aria-label="Search inventory"
			class="w-full bg-transparent text-xs text-white placeholder-slate-400 focus:outline-none"
		/>
		<kbd class="shrink-0 rounded border border-slate-600 px-1 text-[10px] text-slate-400"
			>Ctrl K</kbd
		>
	</div>

	{#if open && query.trim()}
		<button
			class="fixed inset-0 z-30 cursor-default"
			onclick={() => (open = false)}
			aria-label="Close search"
			tabindex="-1"
		></button>
		<div
			class="absolute top-full left-0 z-40 mt-1 w-full overflow-hidden rounded border border-slate-200 bg-white shadow-xl"
		>
			{#if hits.length === 0}
				<div class="px-3 py-2.5 text-xs text-slate-400">No matches.</div>
			{:else}
				<ul class="max-h-96 overflow-y-auto py-1 text-xs">
					{#each hits as h, i (i)}
						<li>
							<button
								onclick={() => pick(h)}
								onmouseenter={() => (active = i)}
								class="flex w-full items-center gap-2 px-3 py-1.5 text-left {i === active
									? 'bg-blue-50'
									: ''}"
							>
								<span
									class="w-20 shrink-0 rounded bg-slate-100 px-1 py-0.5 text-center text-[10px] tracking-wide text-slate-500 uppercase"
									>{kindBadge[h.kind]}</span
								>
								<span class="truncate font-medium text-slate-800">{hitLabel(h)}</span>
								{#if hitHint(h)}
									<span class="ml-auto truncate text-slate-400">{hitHint(h)}</span>
								{/if}
							</button>
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	{/if}
</div>
