// pollWhileVisible runs fn on an interval, but only while the page is visible —
// a backgrounded tab stops hitting the metrics/cluster endpoints. On becoming
// visible again it fires fn once (to refresh stale data) then resumes. Returns a
// cleanup to call on unmount. The caller still does its own initial load.
export function pollWhileVisible(fn: () => void, ms: number): () => void {
	let id: ReturnType<typeof setInterval> | undefined;
	const start = () => {
		id ??= setInterval(fn, ms);
	};
	const stop = () => {
		if (id !== undefined) {
			clearInterval(id);
			id = undefined;
		}
	};
	const onVisibility = () => {
		if (document.visibilityState === 'visible') {
			fn();
			start();
		} else {
			stop();
		}
	};
	document.addEventListener('visibilitychange', onVisibility);
	if (document.visibilityState === 'visible') start();
	return () => {
		stop();
		document.removeEventListener('visibilitychange', onVisibility);
	};
}
