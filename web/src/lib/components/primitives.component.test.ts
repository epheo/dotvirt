import { describe, expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { fireEvent, render, screen } from '@testing-library/svelte';
import Card from './Card.svelte';
import Consequence from './Consequence.svelte';
import CountTile from './CountTile.svelte';
import PageHeader from './PageHeader.svelte';
import StageChip from './StageChip.svelte';
import TabBar from './TabBar.svelte';

// The house primitives every section builds on: what each one says and when
// it tints, so a screen can trust them without re-checking the anatomy.

const text = (s: string) => createRawSnippet(() => ({ render: () => `<span>${s}</span>` }));

describe('Card', () => {
	it('names its section by the title and pads the body unless flush', () => {
		const { container } = render(Card, {
			title: 'Address use',
			meta: '3 of 12',
			link: { label: 'All 12 on the Rules tab', href: '?tab=rules' },
			children: text('body'),
		});
		expect(screen.getByRole('region', { name: 'Address use' })).toBeTruthy();
		expect(screen.getByText('3 of 12')).toBeTruthy();
		expect(screen.getByRole('link', { name: 'All 12 on the Rules tab' }).getAttribute('href')).toBe(
			'?tab=rules',
		);
		expect(container.querySelector('.py-4')?.textContent).toBe('body');
	});

	it('hands a flush body the whole width', () => {
		const { container } = render(Card, { title: 'Top VMs', flush: true, children: text('t') });
		expect(container.querySelector('.py-4')).toBeNull();
	});
});

describe('CountTile', () => {
	it('groups thousands and tints only a problem', () => {
		const { container } = render(CountTile, {
			value: 2980,
			label: 'disks cannot live-migrate',
			tone: 'warn',
			body: 'On local classes.',
			link: { label: 'Show the 2,980 disks', href: '/storage?tab=disks' },
		});
		expect(screen.getByText('2,980')).toBeTruthy();
		expect(container.firstElementChild?.className).toContain('bg-warn-soft');
		expect(screen.getByRole('link', { name: 'Show the 2,980 disks' })).toBeTruthy();
	});

	it('stays untinted for a plain count', () => {
		const { container } = render(CountTile, { value: 214, label: 'VMs keep no data' });
		expect(container.firstElementChild?.className).toContain('bg-panel');
	});

	it('makes the label the headline when there is no number', () => {
		render(CountTile, { label: 'Nothing close to full', tone: 'ok' });
		expect(screen.getByText('Nothing close to full')).toBeTruthy();
		expect(screen.queryByText('undefined')).toBeNull();
	});

	it('runs a callback link as a button', async () => {
		const onclick = vi.fn();
		render(CountTile, {
			value: 611,
			label: 'without a snapshot',
			link: { label: 'Show', onclick },
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Show' }));
		expect(onclick).toHaveBeenCalledOnce();
	});
});

describe('TabBar', () => {
	const tabs = [
		{ id: 'map', label: 'Map' },
		{ id: 'networks', label: 'Networks', count: 131 },
		{ id: 'vms', label: 'VMs', count: 2846 },
	];

	it('marks the current tab and writes counts as plain text', () => {
		render(TabBar, { tabs, active: 'networks', href: (id: string) => `?tab=${id}` });
		const current = screen.getByRole('link', { name: 'Networks 131' });
		expect(current.getAttribute('aria-current')).toBe('page');
		expect(current.className).toContain('font-bold');
		expect(screen.getByRole('link', { name: 'VMs 2,846' }).getAttribute('aria-current')).toBeNull();
	});

	it('drives onchange without an href builder', async () => {
		const onchange = vi.fn();
		render(TabBar, { tabs, active: 'map', onchange });
		await fireEvent.click(screen.getByRole('button', { name: 'VMs 2,846' }));
		expect(onchange).toHaveBeenCalledWith('vms');
	});
});

describe('StageChip', () => {
	it.each([
		['pr', 'Pull request'],
		['now', 'Runs now'],
		['staged', 'staged'],
	] as const)('%s reads %s', (kind, label) => {
		render(StageChip, { kind });
		expect(screen.getByText(label)).toBeTruthy();
	});
});

describe('Consequence', () => {
	it('colors the dot by tone and keeps the sentence', () => {
		const { container } = render(Consequence, {
			tone: 'danger',
			children: text('Detaches all 389 VMs in the same pull request.'),
		});
		expect(screen.getByText('Detaches all 389 VMs in the same pull request.')).toBeTruthy();
		expect(container.querySelector('.bg-danger')).toBeTruthy();
	});
});

describe('PageHeader', () => {
	it('titles the page and states its status', () => {
		// context is also a mount option, so the props go under `props`.
		render(PageHeader, {
			props: {
				title: 'web-net',
				mono: true,
				status: { tone: 'ok', text: 'Project network of team-web' },
				context: '10.40.0.0/16',
			},
		});
		const h1 = screen.getByRole('heading', { level: 1, name: 'web-net' });
		expect(h1.className).toContain('font-mono');
		expect(screen.getByText('Project network of team-web')).toBeTruthy();
		expect(screen.getByText('10.40.0.0/16')).toBeTruthy();
	});

	it('takes a status line with markup', () => {
		render(PageHeader, {
			title: 'web-net',
			status: { tone: 'ok', text: text('Project network of <a href="#">team-web</a>') },
		});
		expect(screen.getByRole('link', { name: 'team-web' })).toBeTruthy();
	});
});
