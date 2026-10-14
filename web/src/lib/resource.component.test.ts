import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { HttpError, Unauthorized } from './api';
import ResourceProbe from '../test/ResourceProbe.svelte';

// resource() as a mounted component sees it: $effects bound to the
// component's lifetime, props as the key source, the DOM as the output.

function deferred<T>() {
	let resolve!: (v: T) => void;
	let reject!: (e: unknown) => void;
	const promise = new Promise<T>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

afterEach(() => {
	vi.useRealTimers();
});

describe('resource in a component', () => {
	it('renders loading, then the data for the current key', async () => {
		const d = deferred<string>();
		render(ResourceProbe, { id: 'vm-a', load: () => d.promise });
		expect(screen.getByText('Loading…')).toBeTruthy();
		d.resolve('data-a');
		expect((await screen.findByTestId('data')).textContent).toBe('data-a');
	});

	it('blanks and reloads on a key change, ignoring the late old response', async () => {
		const inflight = new Map<string, ReturnType<typeof deferred<string>>>();
		const load = (k: string) => {
			const d = deferred<string>();
			inflight.set(k, d);
			return d.promise;
		};
		const { rerender } = render(ResourceProbe, { id: 'vm-a', load });
		await rerender({ id: 'vm-b', load });
		inflight.get('vm-b')!.resolve('data-b');
		expect((await screen.findByTestId('data')).textContent).toBe('data-b');
		inflight.get('vm-a')!.resolve('data-a');
		await Promise.resolve();
		expect(screen.getByTestId('data').textContent).toBe('data-b');
	});

	it('shows a failure, and clears it on a successful refresh', async () => {
		const load = vi
			.fn<(k: string) => Promise<string>>()
			.mockRejectedValueOnce(new HttpError('vm not found', 404))
			.mockResolvedValueOnce('data-a');
		render(ResourceProbe, { id: 'vm-a', load });
		expect((await screen.findByRole('alert')).textContent).toBe('vm not found');
		await fireEvent.click(screen.getByText('Refresh'));
		expect((await screen.findByTestId('data')).textContent).toBe('data-a');
		expect(screen.queryByRole('alert')).toBeNull();
	});

	it('paints no error for a 401: the api layer signs out', async () => {
		const load = vi.fn().mockRejectedValue(new Unauthorized());
		render(ResourceProbe, { id: 'vm-a', load });
		await vi.waitFor(() => expect(load).toHaveBeenCalled());
		await Promise.resolve();
		expect(screen.queryByRole('alert')).toBeNull();
	});

	it('polls while mounted and stops on unmount', async () => {
		vi.useFakeTimers();
		const load = vi.fn().mockResolvedValue('tick');
		const { unmount } = render(ResourceProbe, { id: 'vm-a', load, poll: 1000 });
		expect(load).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(3000);
		expect(load).toHaveBeenCalledTimes(4);
		unmount();
		await vi.advanceTimersByTimeAsync(5000);
		expect(load).toHaveBeenCalledTimes(4);
	});
});
