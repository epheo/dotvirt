import { api, Unauthorized, type DiskProtection, type Replication } from '#lib/api.js';
import { friendlyError } from '#lib/format.js';
import { modules } from './modules.svelte';

// Only a known no hides the tab, as with every module.
export const replicationOff = () => modules.value !== null && !modules.value.replicatedStorage;

// Pulled when the tab opens and when the frame's watermark moves, never polled.
// An operator reads the placement, anyone else their own disks' standing.
class ReplicationStore {
	overview = $state.raw<Replication | null>(null);
	protection = $state.raw<DiskProtection[] | null>(null);
	error = $state('');
	#gen = 0;

	async pull(operator: boolean) {
		const gen = ++this.#gen;
		try {
			if (operator) {
				const v = await api.replication();
				if (gen === this.#gen) this.overview = v;
			} else {
				const v = await api.protection();
				if (gen === this.#gen) this.protection = v;
			}
			if (gen === this.#gen) this.error = '';
		} catch (e) {
			if (gen !== this.#gen || e instanceof Unauthorized) return; // 401 signs out centrally
			this.error = friendlyError(e);
		}
	}

	reset() {
		this.#gen++;
		this.overview = null;
		this.protection = null;
		this.error = '';
	}
}

export const replication = new ReplicationStore();
