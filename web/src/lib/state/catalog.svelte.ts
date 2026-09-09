import { api, Unauthorized, type Template } from '$lib/api';
import { friendlyError } from '$lib/format';

// The template library, shared by the Catalog tree and workspace so both list
// the same items from one fetch. The platform kinds come from inventory.options.
class Catalog {
	templates = $state<Template[] | null>(null);
	error = $state('');
	#inflight: Promise<void> | null = null;

	// Concurrent callers (tree + workspace mounting together) share one request.
	load(): Promise<void> {
		if (this.#inflight) return this.#inflight;
		this.#inflight = api
			.templates()
			.then((t) => {
				this.templates = t.templates;
				this.error = '';
			})
			.catch((e) => {
				if (e instanceof Unauthorized) return; // signed out centrally
				this.error = friendlyError(e);
			})
			.finally(() => {
				this.#inflight = null;
			});
		return this.#inflight;
	}

	reset() {
		this.templates = null;
		this.error = '';
	}
}

export const catalog = new Catalog();
