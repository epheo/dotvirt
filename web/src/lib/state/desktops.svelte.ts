import { api, Unauthorized, type Desktops } from '#lib/api.js';
import { friendlyError } from '#lib/format.js';
import { inventory } from './inventory.svelte';

// Pulled while a Desktops page is open and when the frame's watermark moves,
// never polled.
class DesktopsStore {
	value = $state.raw<Desktops | null>(null);
	error = $state('');
	#gen = 0;

	async pull() {
		const gen = ++this.#gen;
		try {
			const v = await api.desktops();
			if (gen !== this.#gen) return;
			this.value = v;
			this.error = '';
		} catch (e) {
			if (gen !== this.#gen || e instanceof Unauthorized) return; // 401 signs out centrally
			this.error = friendlyError(e);
		}
	}

	reset() {
		this.#gen++;
		this.value = null;
		this.error = '';
	}
}

export const desktops = new DesktopsStore();

// A caller who may list no DeskuBe kind gets an answer that never syncs and a
// watermark of 0, as on a cluster without DeskuBe: nothing to read, not still
// reading.
export const nothingReadable = () =>
	desktops.value !== null && !desktops.value.synced && inventory.desktopsVersion === 0;
