import { browser } from '$app/environment';

// A $state-backed value that survives reloads. Whole-value assignment only —
// callers replace objects (`p.value = { ...p.value, key: v }`), which keeps
// writes explicit and the storage sync trivial. Keys live under `dotvirt.`.
export function persisted<T>(key: string, initial: T): { value: T } {
	let value = $state(initial);
	if (browser) {
		try {
			const raw = localStorage.getItem(key);
			if (raw !== null) value = JSON.parse(raw) as T;
		} catch {
			// corrupt entry — fall back to the initial value
		}
	}
	return {
		get value() {
			return value;
		},
		set value(v: T) {
			value = v;
			if (browser)
				try {
					localStorage.setItem(key, JSON.stringify(v));
				} catch {
					// storage full or blocked — keep the in-memory value
				}
		},
	};
}
