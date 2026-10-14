import { api, Unauthorized, type Template } from '$lib/api';
import { friendlyError } from '$lib/format';

// The template library, shared by the Catalog tree and workspace so both list
// the same items from one fetch. The platform kinds come from inventory.options.
class Catalog {
	templates = $state<Template[] | null>(null);
	error = $state('');
	#inflight: Promise<void> | null = null;
	// Bumped by reset(), so a load started before a sign-out drops its late
	// response instead of repopulating the reset store.
	#gen = 0;

	// Concurrent callers (tree + workspace mounting together) share one request.
	load(): Promise<void> {
		if (this.#inflight) return this.#inflight;
		const gen = this.#gen;
		const pull = api
			.templates()
			.then((t) => {
				if (gen !== this.#gen) return;
				this.templates = t.templates;
				this.error = '';
			})
			.catch((e) => {
				if (gen !== this.#gen || e instanceof Unauthorized) return; // 401 signs out centrally
				this.error = friendlyError(e);
			})
			.finally(() => {
				if (this.#inflight === pull) this.#inflight = null;
			});
		this.#inflight = pull;
		return pull;
	}

	reset() {
		this.#gen++;
		this.#inflight = null;
		this.templates = null;
		this.error = '';
	}
}

export const catalog = new Catalog();
