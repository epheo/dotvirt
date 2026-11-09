import { expect, login, setScenario, test } from './fx';

// The Networking section: the map lights one VM's way out and inspects it,
// problems sit on their nodes, a network's page answers where it connects and
// stages its changes, and the firewall reads as sentences.

test('finding a VM lights its way out and opens the inspector', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking');
	await page.getByLabel('Find a VM, network or IP').fill('db-1');
	const inspector = page.getByRole('complementary', { name: /Inspector, db-1/ });
	await expect(inspector).toBeVisible();
	await expect(inspector.getByText("Cluster edge. Source IP becomes worker-1's.")).toBeVisible();
	await expect(
		inspector.getByText('Accepts TCP 5432 from VMs labeled app=web-1 in web-prod.'),
	).toBeVisible();
	await expect(page).toHaveURL(/\/networking$/);
	await inspector.getByRole('button', { name: 'Check connectivity' }).click();
	await expect(page.getByRole('dialog', { name: 'Check connectivity from db-1' })).toBeVisible();
	await page.keyboard.press('Escape');
	await inspector.getByRole('button', { name: 'Close inspector' }).click();
	await expect(inspector).toHaveCount(0);
});

test('at scale the map aggregates and problems carry their fix', async ({ page }) => {
	test.setTimeout(90_000);
	await setScenario(page, 'scale');
	await login(page);
	await page.goto('/networking');
	const main = page.locator('main');
	await expect(main.getByText('Showing 6 of 27 networks and 5 of 52 projects.')).toBeVisible();
	await expect(main.getByText('47 more projects')).toBeVisible();
	await expect(main.getByText('Fails on host-41')).toBeVisible();
	await expect(
		main.getByText('udn-frontend and udn-tcp-frontend both use 10.200.0.0/16.'),
	).toBeVisible();
	await main.getByRole('button', { name: /Problems only/ }).click();
	await expect(page).toHaveURL(/problems=1/);
	await expect(main.getByText('47 more projects')).toHaveCount(0);
	await expect(main.getByText('Cut off on host-41')).toBeVisible();
	// The bell agrees with the header.
	await expect(page.locator('button[title^="Issues"]')).toHaveText(/2 issues/);
});

test('a network page answers where it connects and stages an attach', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking/blue-secondary');
	const main = page.locator('main');
	await expect(main.getByRole('heading', { name: 'Where it connects' })).toBeVisible();
	await expect(main.getByText('Shared network, 3 projects')).toBeVisible();
	await expect(main.getByText('Attach as blue-secondary in a VM manifest.')).toBeVisible();
	await main.getByRole('button', { name: 'Attach VMs' }).click();
	const dialog = page.getByRole('dialog', { name: 'Attach VMs to blue-secondary' });
	await dialog.getByRole('checkbox').first().check();
	await dialog.getByRole('button', { name: 'Stage 1 attachment' }).click();
	await expect(page.getByText(/Attached to blue-secondary: staged 1 of 1/)).toBeVisible();
});

test('the firewall reads as sentences, cluster rules first', async ({ page }) => {
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking?tab=firewall');
	const main = page.locator('main');
	await expect(main.getByText('Blocks all traffic from VMs in any namespace.')).toBeVisible();
	await main.getByLabel('Filter rules').fill('5432');
	await expect(main.getByText('allow-https')).toHaveCount(0);
	await expect(main.getByText('allow-web-to-db')).toBeVisible();
});

test('rules that merely match any namespace fold into one line', async ({ page }) => {
	// On a real cluster the platform's own rules let any namespace in: they
	// must not read as this network's rules, and REACHES never shows them.
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking/batch-prod/udn-tcp-frontend');
	const main = page.locator('main');
	await expect(main.getByText(/^No rule of its own: every VM here accepts traffic/)).toBeVisible();
	await expect(
		main.getByText(/6 rules in other projects let any namespace reach their own VMs/),
	).toBeVisible();
	await expect(main.getByText(/openshift-gitops/)).toHaveCount(0);
	await main.getByRole('link', { name: 'All on the Rules tab' }).click();
	await expect(
		main.getByRole('heading', { name: 'Rules elsewhere that let any namespace in' }),
	).toBeVisible();
	await expect(main.getByText(/openshift-gitops on TCP 8082/)).toBeVisible();
	await expect(main.getByRole('heading', { name: 'Rules elsewhere that name it' })).toHaveCount(0);
});

test('a tenant counts and draws only the networks it can see', async ({ page }) => {
	// The catalog a tenant gets holds its project networks and the shared ones
	// published to it; every count and lane comes from it, never from the
	// cluster's whole.
	await setScenario(page, 'tenant');
	await login(page);
	await page.goto('/networking');
	const main = page.locator('main');
	const aside = page.locator('aside');
	await expect(main.getByText('2 VMs, 7 networks')).toBeVisible();
	await expect(main.getByRole('link', { name: /^Networks\s*7$/ })).toBeVisible();
	await expect(aside.getByText('Networks', { exact: true }).locator('..')).toContainText('7');
	for (const foreign of ['dc-vlan-net', 'internal-l2', 'udn-tcp-frontend', 'team-db'])
		await expect(page.getByText(foreign)).toHaveCount(0);
	await main.getByRole('link', { name: /^Networks/ }).click();
	await expect(main.getByText('7 of 7', { exact: true })).toBeVisible();
	await expect(page.getByText('dc-vlan-net')).toHaveCount(0);
});

test('a raw network attachment belongs to the platform repo', async ({ page }) => {
	// A project's repo may not hold one, so the page never offers to adopt it
	// there, and says why.
	await setScenario(page, 'base');
	await login(page);
	await page.goto('/networking/web-prod/legacy-l2');
	const main = page.locator('main');
	await expect(main.getByText('Network attachments belong to the platform repo.')).toBeVisible();
	await expect(main.getByRole('button', { name: 'Adopt into git' })).toHaveCount(0);
});

test("objects a project's repo cannot keep are issues with their next step", async ({ page }) => {
	await setScenario(page, 'drift');
	await login(page);
	await page.locator('button[title^="Issues"]').click();
	const bell = page.getByRole('banner');
	await expect(
		bell.getByRole('button', { name: /allow-https.*no namespace in git/ }),
	).toBeVisible();
	await expect(bell.getByRole('button', { name: /web-9.*outside team-web/ })).toBeVisible();
	await bell.getByRole('button', { name: /legacy-l2.*only the platform repo may hold/ }).click();
	await expect(page).toHaveURL(/\/networking\/web-prod\/legacy-l2$/);
	await expect(
		page.locator('main').getByText(/team-web's repo declares it, but network attachments/),
	).toBeVisible();
});

test('a rule on another project reads its subject in the words the caller may see', async ({
	page,
}) => {
	await setScenario(page, 'scale');
	await login(page);
	const main = page.locator('main');
	await page.goto('/networking/web-prod/web-net');
	await expect(main.getByText('TCP 5432 to VMs labeled app=db-1 in db-prod')).toBeVisible();

	// A tenant's view of the same rule: the target comes back redacted. The page
	// says what the server's applies line says, never the target.
	await page.route('**/api/networks/rules?*', async (route) => {
		const body = await (await route.fetch()).json();
		for (const r of body.reaches ?? [])
			if (r.policy.namespace === 'db-prod') {
				r.policy.target = 'tenant-a, other namespaces';
				r.policy.applies = 'other namespaces';
			}
		await route.fulfill({ json: body });
	});
	await page.reload();
	await expect(main.getByText('TCP 5432 to other namespaces')).toBeVisible();
	await expect(main.getByText(/VMs labeled tenant-a/)).toHaveCount(0);
});

test('a cluster rule the caller may not read names no object and links nowhere', async ({
	page,
}) => {
	await setScenario(page, 'withheld');
	await login(page);
	const main = page.locator('main');
	await page.goto('/compute/team-web/web-prod?tab=security');
	await expect(main.getByText('cluster rule, priority 10').first()).toBeVisible();
	// The label carries the priority; the tier pill does not repeat it.
	await expect(main.getByText('Cluster rule, priority 10', { exact: true })).toHaveCount(0);
	await page.goto('/networking/web-prod/web-net');
	await expect(main.getByText(/Cluster rule, priority 10/).first()).toBeVisible();
	await expect(main.getByRole('link', { name: /cluster rule, priority/ })).toHaveCount(0);
});
