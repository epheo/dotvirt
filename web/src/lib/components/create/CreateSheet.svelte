<script lang="ts">
	import { Copy, Disc, Folder, LogOut, Monitor, Network, Shield, X } from '@lucide/svelte';
	import { clusterGate, clusterObjectGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import {
		SHEET_TYPES,
		SheetCtl,
		provideSheet,
		typeOf,
		type CreateModal,
		type SheetType,
	} from './sheet.svelte.js';
	import VMForm from './VMForm.svelte';
	import TemplateForm from './TemplateForm.svelte';
	import ImageForm from './ImageForm.svelte';
	import NetworkForm from './NetworkForm.svelte';
	import RuleForm from './RuleForm.svelte';
	import OutboundForm from './OutboundForm.svelte';
	import ProjectForm from './ProjectForm.svelte';

	// One right-side sheet for every create: the chips switch what it makes,
	// the form keeps the place the user came from, and what the change stages
	// stays in view beside the form. It sits between the masthead and the dock
	// so the page it was opened from stays in sight under the shade.
	let { modal, onclose }: { modal: CreateModal; onclose: () => void } = $props();

	const ICONS = {
		vm: Monitor,
		template: Copy,
		image: Disc,
		network: Network,
		rule: Shield,
		outbound: LogOut,
		project: Folder,
	};

	// svelte-ignore state_referenced_locally
	let type = $state<SheetType>(typeOf(modal));
	// The place survives a chip switch; everything else is per type.
	// svelte-ignore state_referenced_locally
	let namespace = $state(('namespace' in modal && modal.namespace) || '');

	// An object read back from git turns the sheet into its edit form, and an
	// edit cannot change what it is.
	const editing = $derived(
		'initial' in modal && !!modal.initial
			? (modal.initial as { name?: string; namespace?: string; spec?: { name?: string } })
			: null,
	);
	const editName = $derived(editing?.name ?? editing?.spec?.name ?? editing?.namespace ?? '');

	// A type is offered where the caller may make one somewhere: the cluster
	// kinds by their own field; the namespaced ones while some namespace takes
	// changes (the frame's signal), each form then holding its chosen place to
	// that kind's own field.
	const noRepo = $derived(!inventory.namespaces.length);
	const can = (resource: string) => clusterObjectGate(resource, 'create').ok;
	const allowed: Record<SheetType, boolean> = $derived({
		vm: !noRepo,
		template: !noRepo,
		image: !noRepo,
		network: !noRepo || can('network'),
		rule: !noRepo || can('adminnetworkpolicy') || can('baselineadminnetworkpolicy'),
		outbound: can('egressip') || can('externalroute'),
		project: clusterGate.namespaceCreate().ok,
	});
	const chips = $derived(SHEET_TYPES.filter((t) => allowed[t.id] || t.id === type));

	const title = $derived(
		editing
			? `Change ${editName}`
			: modal.kind === 'newProject' && modal.adopt
				? `Adopt ${modal.adopt}`
				: SHEET_TYPES.find((t) => t.id === type)!.title,
	);
	const from = $derived(
		('from' in modal && modal.from) ||
			(modal.kind === 'upload' && modal.file ? `${modal.file.name}, the file you dropped` : '') ||
			(modal.kind === 'newVM' && modal.image
				? `${modal.image.split('/').pop()}, the image you picked`
				: '') ||
			('namespace' in modal && modal.namespace ? `${modal.namespace}, where you were` : ''),
	);
	// The opener's payload belongs to its own type; another chip starts from the place alone.
	const own = $derived(type === typeOf(modal) ? modal : null);

	const ctl = new SheetCtl();
	provideSheet(ctl);
	const dismiss = () => {
		if (!ctl.pinned) onclose();
	};

	// Between the masthead and the dock: measured, so a banner or an open dock
	// moves the sheet instead of covering it.
	let box = $state({ top: 0, bottom: 0 });
	$effect(() => {
		const measure = () => {
			const r = document.querySelector('[data-workspace]')?.getBoundingClientRect();
			box = r ? { top: r.top, bottom: window.innerHeight - r.bottom } : { top: 0, bottom: 0 };
		};
		measure();
		const ro = new ResizeObserver(measure);
		const ws = document.querySelector('[data-workspace]');
		if (ws) ro.observe(ws);
		window.addEventListener('resize', measure);
		return () => {
			ro.disconnect();
			window.removeEventListener('resize', measure);
		};
	});

	let panel = $state<HTMLElement>();
	const FOCUSABLE =
		'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

	// Focus lands in the form on open and returns to the opener on close.
	$effect(() => {
		if (!panel) return;
		const opener = document.activeElement as HTMLElement | null;
		(panel.querySelector<HTMLElement>('[data-autofocus]') ?? panel).focus();
		return () => opener?.focus();
	});

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && !e.defaultPrevented) {
			e.preventDefault();
			dismiss();
			return;
		}
		if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && ctl.submit) {
			e.preventDefault();
			ctl.submit();
			return;
		}
		if (e.key !== 'Tab' || !panel) return;
		const items = [...panel.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
			(el) => el.offsetParent !== null,
		);
		if (!items.length) return;
		const first = items[0];
		const last = items[items.length - 1];
		if (e.shiftKey && (document.activeElement === first || document.activeElement === panel)) {
			e.preventDefault();
			last.focus();
		} else if (!e.shiftKey && document.activeElement === last) {
			e.preventDefault();
			first.focus();
		}
	}

	function pick(t: SheetType) {
		if (ctl.pinned) return;
		type = t;
	}

	// Arrow keys move along the chips, as a radio group does.
	function chipKeys(e: KeyboardEvent) {
		if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
		e.preventDefault();
		const i = chips.findIndex((c) => c.id === type);
		const next = chips[(i + (e.key === 'ArrowRight' ? 1 : chips.length - 1)) % chips.length];
		pick(next.id);
		(e.currentTarget as HTMLElement)
			.querySelector<HTMLElement>(`[data-chip="${next.id}"]`)
			?.focus();
	}
</script>

<div
	class="fixed inset-x-0 z-50 flex justify-end bg-bar/45"
	style:top="{box.top}px"
	style:bottom="{box.bottom}px"
	onclick={(e) => e.target === e.currentTarget && dismiss()}
	role="presentation"
>
	<div
		bind:this={panel}
		role="dialog"
		aria-modal="true"
		aria-labelledby="create-title"
		tabindex="-1"
		{onkeydown}
		class="flex h-full w-[840px] max-w-full flex-col bg-panel shadow-[-16px_0_40px_rgba(0,0,0,0.28)] outline-none"
	>
		<header class="flex flex-col gap-3.5 border-b border-line-soft px-6 pt-5 pb-4">
			<div class="flex items-start gap-3">
				<div class="min-w-0 flex-1">
					<h2 id="create-title" class="text-xl leading-7 font-bold text-ink">{title}</h2>
					{#if from && type !== 'project'}<p class="text-sm text-ink-muted">
							Filled in from {from}.
						</p>{/if}
				</div>
				<button
					type="button"
					onclick={dismiss}
					disabled={ctl.pinned}
					aria-label="Close"
					class="flex size-8 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong disabled:opacity-40"
					><X size={18} /></button
				>
			</div>
			{#if !editing && !(modal.kind === 'newProject' && modal.adopt)}
				<div
					class="flex flex-wrap gap-1.5"
					role="radiogroup"
					aria-label="What to create"
					tabindex="-1"
					onkeydown={chipKeys}
				>
					{#each chips as c (c.id)}
						{@const Icon = ICONS[c.id]}
						{@const on = c.id === type}
						<button
							type="button"
							role="radio"
							aria-checked={on}
							tabindex={on ? 0 : -1}
							data-chip={c.id}
							disabled={ctl.pinned && !on}
							onclick={() => pick(c.id)}
							class="inline-flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-[13px] whitespace-nowrap disabled:opacity-50 {on
								? 'border-accent bg-select font-bold text-accent-deep dark:text-accent-ink'
								: 'border-line bg-panel text-ink hover:border-ink-faint'}"
						>
							<Icon size={14} class={on ? 'text-accent-ink' : 'text-ink-muted'} />{c.label}
						</button>
					{/each}
				</div>
			{/if}
		</header>

		{#key type}
			{#if type === 'vm'}
				<VMForm
					bind:namespace
					namespaces={own?.kind === 'newVM' ? own.namespaces : null}
					image={own?.kind === 'newVM' ? own.image : undefined}
					{onclose}
				/>
			{:else if type === 'template'}
				<TemplateForm
					bind:namespace
					library={own?.kind === 'deployTemplate' ? own.library : undefined}
					template={own?.kind === 'deployTemplate' ? own.template : undefined}
					{onclose}
				/>
			{:else if type === 'image'}
				<ImageForm
					bind:namespace
					file={own?.kind === 'upload' ? own.file : undefined}
					bootImage={own?.kind === 'upload' ? own.bootImage : undefined}
					{onclose}
				/>
			{:else if type === 'network'}
				<NetworkForm
					bind:namespace
					initial={own?.kind === 'newNetwork' ? own.initial : undefined}
					like={own?.kind === 'newNetwork' ? own.like : undefined}
					{onclose}
				/>
			{:else if type === 'rule'}
				<RuleForm bind:namespace modal={own} {onclose} />
			{:else if type === 'outbound'}
				<OutboundForm
					bind:namespace
					initial={own?.kind === 'clusterEdge' ? own.initial : undefined}
					{onclose}
				/>
			{:else}
				<ProjectForm adopt={own?.kind === 'newProject' ? own.adopt : undefined} {onclose} />
			{/if}
		{/key}
	</div>
</div>
