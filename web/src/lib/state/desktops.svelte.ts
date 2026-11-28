import { untrack } from 'svelte';
import { api, Unauthorized, type Desktops, type Project } from '#lib/api.js';
import { projectOf, rights } from '#lib/desktops.js';
import { friendlyError } from '#lib/format.js';
import { inventory } from './inventory.svelte';

// Pulled while the Desktops tree shows and when the frame's watermark moves,
// never polled.
class DesktopsStore {
	value = $state.raw<Desktops | null>(null);
	error = $state('');
	#gen = 0;

	// What the section reads of the projects: names, titles, repos and
	// namespaces. Keyed on them, so a VM frame rebuilds no desktops view.
	readonly #layoutKey = $derived(
		inventory.projects
			.map((p) =>
				[
					p.name,
					p.title ?? '',
					p.repo ?? '',
					p.repoPage ?? '',
					...p.namespaces.map((n) => n.namespace),
				].join('\u0001'),
			)
			.join('\u0000'),
	);
	readonly projects = $derived.by((): readonly Project[] => {
		void this.#layoutKey;
		return untrack(() => inventory.projects);
	});
	readonly owners = $derived(projectOf(this.projects));
	// The namespaces the answer covers: those of the projects with a repo when
	// it was asked for. Taken from the frame instead, a project gaining a repo
	// would read empty until the pull it starts lands.
	covered = $state.raw<ReadonlySet<string>>(new Set());
	readonly may = $derived(this.value ? rights(this.value, this.covered) : null);

	title(project: string): string {
		return this.projects.find((p) => p.name === project)?.title || project;
	}

	async pull() {
		const gen = ++this.#gen;
		const covered = new Set(
			untrack(() => this.projects)
				.filter((p) => p.repo)
				.flatMap((p) => p.namespaces.map((n) => n.namespace)),
		);
		try {
			const v = await api.desktops();
			if (gen !== this.#gen) return;
			this.value = v;
			this.covered = covered;
			this.error = '';
		} catch (e) {
			if (gen !== this.#gen || e instanceof Unauthorized) return; // 401 signs out centrally
			this.error = friendlyError(e);
		}
	}

	reset() {
		this.#gen++;
		this.value = null;
		this.covered = new Set();
		this.error = '';
	}
}

export const desktops = new DesktopsStore();

// A caller who may list no DeskuBe kind gets an answer that never syncs and a
// watermark of 0, as on a cluster without DeskuBe: nothing to read, not still
// reading.
export const nothingReadable = () =>
	desktops.value !== null && !desktops.value.synced && inventory.desktopsVersion === 0;
