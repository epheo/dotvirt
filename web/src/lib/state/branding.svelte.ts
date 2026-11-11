import { api, type Branding } from '#lib/api.js';

// GET /api/branding, read once at boot, before sign-in: a provider's dress for
// the console. Null (not read yet, or the read failed) keeps dotvirt's own.
class BrandingStore {
	value = $state.raw<Branding | null>(null);
	get name() {
		return this.value?.productName || 'dotvirt';
	}
	async load() {
		try {
			this.value = await api.branding();
		} catch {
			this.value = null;
		}
	}
}
export const branding = new BrandingStore();
