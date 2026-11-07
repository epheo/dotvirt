import { expect, login, setScenario, test } from './fx';
import type { Page } from '@playwright/test';

// The create sheet: one place for every New, prefilled from where the user
// was, saying what it stages before it does.

async function openNew(page: Page, path = '/compute/team-web/web-prod') {
	await setScenario(page, 'base');
	await login(page);
	await page.goto(path);
	await page.getByTitle('Create a VM, network, rule or project').click();
	return page.getByRole('dialog');
}

test('a VM stages the request the backend renders, then shows its password once', async ({
	page,
}) => {
	const sheet = await openNew(page);
	await expect(sheet.getByRole('heading', { name: 'New VM' })).toBeVisible();
	await expect(sheet.getByLabel('Name', { exact: true })).toHaveValue('web-3');
	await expect(sheet.getByText('Free. Next after web-2.')).toBeVisible();
	await expect(sheet.getByText('Fits on 2 of 3 hosts. worker-3 is in maintenance.')).toBeVisible();
	await expect(sheet.getByText('web-prod/web-3.yaml')).toBeVisible();

	// Keeping live migration is one click: the root disk moves to a shared class.
	await sheet.getByRole('button', { name: 'Use bulk-hdd to keep it' }).click();
	await expect(sheet.getByText('web-3 can live-migrate.')).toBeVisible();

	const posted = page.waitForRequest(
		(r) =>
			r.method() === 'POST' &&
			new URL(r.url()).pathname === '/api/vms' &&
			!r.url().includes('dryRun'),
	);
	await sheet.getByRole('button', { name: 'Stage VM' }).click();
	const body = (await posted).postDataJSON();
	expect(body).toMatchObject({
		name: 'web-3',
		namespace: 'web-prod',
		instancetype: 'u1.medium',
		preference: 'fedora',
		osImage: { name: 'fedora-42', namespace: 'dotvirt-images' },
		diskSize: '30Gi',
		storageClass: 'bulk-hdd',
		running: true,
		primaryNetwork: true,
		cloudInit: { user: 'fedora' },
	});
	expect(body.cloudInit.password).toMatch(/^[a-zA-Z0-9]{16}$/);

	// The sheet stays pinned on the one-time reveal until Done.
	await expect(sheet.getByText(body.cloudInit.password)).toBeVisible();
	await page.keyboard.press('Escape');
	await expect(sheet).toBeVisible();
	await sheet.getByRole('button', { name: 'Done' }).click();
	await expect(sheet).toBeHidden();
});

test('Show YAML renders through the dry run and stages nothing', async ({ page }) => {
	const sheet = await openNew(page);
	const dry = page.waitForRequest((r) => r.url().includes('/api/vms?dryRun=true'));
	await sheet.getByRole('button', { name: 'Show YAML' }).click();
	expect((await dry).postDataJSON().name).toBe('web-3');
	await expect(sheet.getByText('kind: VirtualMachine')).toBeVisible();
	await expect(sheet.getByText('name: u1.medium')).toBeVisible();
	const draft = await page.request.get('/api/draft?project=team-web');
	expect((await draft.json()).count).toBe(0);
});

test('a Change editor searches, and one editor is open at a time', async ({ page }) => {
	const sheet = await openNew(page);
	await sheet.getByRole('button', { name: 'Change network' }).click();
	await page.keyboard.type('frontend');
	await expect(sheet.getByText(/1 of 5 networks match/)).toBeVisible();
	await sheet.getByRole('checkbox', { name: /udn-frontend/ }).check();
	await sheet.getByRole('button', { name: 'Change size' }).click();
	await expect(sheet.getByRole('button', { name: 'Change network' })).toBeVisible();
	await expect(sheet.getByText('web-net, udn-frontend')).toBeVisible();
	await sheet.getByRole('radio', { name: /Large, 4 vCPU 8 GiB/ }).check();
	await page.keyboard.press('Escape');
	await expect(sheet).toBeVisible();
	await expect(sheet.getByRole('button', { name: 'Change size' })).toBeFocused();
	await expect(sheet.getByText('Large, 4 vCPU 8 GiB')).toBeVisible();
});

test('the chips switch what to make and keep the place', async ({ page }) => {
	const sheet = await openNew(page);
	await sheet.getByRole('radio', { name: 'VM from template' }).click();
	await expect(sheet.getByRole('heading', { name: 'New VM from template' })).toBeVisible();
	await expect(sheet.getByLabel('Project and namespace')).toHaveText(/web-prod/);
	await expect(sheet.getByRole('radio', { name: /web-node/ })).toBeChecked();
	await sheet.getByLabel('Role').fill('frontend');
	const posted = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().endsWith('/api/templates/deploy'),
	);
	await page.keyboard.press('Control+Enter');
	expect((await posted).postDataJSON()).toMatchObject({
		library: 'team-web',
		template: 'web-node',
		namespace: 'web-prod',
		parameters: { ROLE: 'frontend' },
		powerOn: true,
	});
	await expect(sheet).toBeHidden();
});

test('a taken range names its networks and offers free ones', async ({ page }) => {
	const sheet = await openNew(page, '/networking');
	await expect(sheet.getByRole('heading', { name: 'New network' })).toBeVisible();
	await sheet.getByLabel('Name', { exact: true }).fill('web-backend');
	await expect(sheet.getByText('Free in team-web.')).toBeVisible();
	await sheet.getByLabel('Subnet').fill('10.200.0.0/16');
	// Only a network the new one can meet is a clash; the isolated reuse in
	// another namespace is a quiet line.
	const warn = sheet.getByText(/Already used by/);
	await expect(warn).toContainText('Already used by udn-frontend.');
	await expect(
		sheet.getByText(/udn-tcp-frontend uses this range in another namespace/),
	).toBeVisible();
	await expect(sheet.getByText('Overlaps udn-frontend.')).toBeVisible();
	await sheet.getByRole('button', { name: 'Use 10.201.0.0/16' }).click();
	await expect(warn).toBeHidden();
	await expect(sheet.getByLabel('Subnet')).toHaveValue('10.201.0.0/16');

	await sheet.getByRole('button', { name: 'Show YAML' }).click();
	await expect(sheet.getByText('kind: UserDefinedNetwork')).toBeVisible();
	const posted = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().endsWith('/api/networks'),
	);
	await sheet.getByRole('button', { name: 'Stage network' }).click();
	expect((await posted).postDataJSON()).toEqual({
		name: 'web-backend',
		namespace: 'web-prod',
		scope: 'project',
		subnets: ['10.201.0.0/16'],
	});
	await expect(sheet).toBeHidden();
});

test('a project rule says what it protects and refuses', async ({ page }) => {
	const sheet = await openNew(page, '/networking?tab=security');
	await expect(sheet.getByRole('heading', { name: 'New firewall rule' })).toBeVisible();
	await sheet.getByLabel('Name', { exact: true }).fill('allow-web');
	await sheet.getByLabel('Label key', { exact: true }).fill('app');
	await sheet.getByLabel('Label value', { exact: true }).fill('web-1');
	await expect(sheet.getByText('Applies to 1 VM in web-prod.')).toBeVisible();
	await sheet.getByLabel('Source 1 port').fill('443');
	const posted = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().endsWith('/api/networkpolicies'),
	);
	await sheet.getByRole('button', { name: 'Stage rule' }).click();
	expect((await posted).postDataJSON()).toEqual({
		name: 'allow-web',
		namespace: 'web-prod',
		appliedTo: { app: 'web-1' },
		ingress: [{ ports: [{ protocol: 'TCP', port: 443 }] }],
	});
});

test('an image uploads now as a boot image, then a VM boots from it', async ({ page }) => {
	const sheet = await openNew(page, '/storage');
	await expect(sheet.getByRole('heading', { name: 'New image' })).toBeVisible();
	await expect(sheet.getByText('Runs now')).toBeVisible();
	await expect(sheet.getByRole('checkbox', { name: /Offer it as a boot image/ })).toBeChecked();
	await sheet.locator('input[type=file]').setInputFiles({
		name: 'Fedora-43.qcow2',
		mimeType: 'application/octet-stream',
		buffer: Buffer.alloc(1024),
	});
	await expect(sheet.getByLabel('Name', { exact: true })).toHaveValue('fedora-43');
	const posted = page.waitForRequest(
		(r) => r.method() === 'POST' && r.url().endsWith('/api/uploads'),
	);
	await sheet.getByRole('button', { name: 'Upload image' }).click();
	expect((await posted).postDataJSON()).toMatchObject({ name: 'fedora-43', bootImage: true });
	await expect(sheet.getByText(/is a boot image in/)).toBeVisible({ timeout: 15_000 });
	await sheet.getByRole('button', { name: 'New VM from it' }).click();
	await expect(sheet.getByRole('heading', { name: 'New VM' })).toBeVisible();
	await expect(sheet.getByText('Filled in from fedora-43, the image you picked.')).toBeVisible();
	await expect(sheet.getByText('fedora-43', { exact: true })).toBeVisible();
	const posted2 = page.waitForRequest(
		(r) =>
			r.method() === 'POST' &&
			new URL(r.url()).pathname === '/api/vms' &&
			!r.url().includes('dryRun'),
	);
	await sheet.getByRole('button', { name: 'Stage VM' }).click();
	expect((await posted2).postDataJSON().osImage).toEqual({
		name: 'fedora-43',
		namespace: 'web-prod',
	});
});

test('Escape closes the sheet and hands focus back to New', async ({ page }) => {
	const sheet = await openNew(page);
	await page.keyboard.press('Escape');
	await expect(sheet).toBeHidden();
	await expect(page.getByRole('button', { name: /^New$/ })).toBeFocused();
});
