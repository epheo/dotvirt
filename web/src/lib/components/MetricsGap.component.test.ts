import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import MetricsGap from './MetricsGap.svelte';

describe('MetricsGap', () => {
	it('says nothing over a whole read', () => {
		const { container } = render(MetricsGap, { reason: undefined });
		expect(container.textContent?.trim()).toBe('');
	});

	it('marks a partial read incomplete', () => {
		render(MetricsGap, { reason: 'partial' });
		expect(screen.getByText(/^Incomplete: /)).toBeTruthy();
	});

	it('asks a wide tenant to narrow to a project', () => {
		render(MetricsGap, { reason: 'scope' });
		expect(screen.getByText(/Pick a project to see its metrics/)).toBeTruthy();
	});
});
