// One parameterized Playwright screenshot driver for every scene the old
// per-purpose *-shot.mjs one-offs covered.
//   node shot.mjs <scene>   runs one scene
//   node shot.mjs --list    prints the scene table
// Shared env: OC_TOKEN (else `oc whoami -t`), OUT (default /tmp), BASE /
// BASE_URL where a scene serves a non-default origin. Per-scene extras are
// noted in the table.
import { execSync } from 'node:child_process';
import { chromium } from 'playwright';

const OUT = process.env.OUT || '/tmp';
const DEV = 'http://localhost:5173';

function token() {
	return process.env.OC_TOKEN || execSync('oc whoami -t', { encoding: 'utf8' }).trim();
}

// Plain login: fill the token textarea and submit.
async function login(page, base) {
	await page.goto(base);
	await page.waitForSelector('textarea');
	await page.fill('textarea', token());
	await page.click('button[type="submit"]');
}

// The table header paints immediately, but rows only arrive with the WS
// inventory frame. Gate on an actual VM row so we never snap the empty
// "No VMs in scope" grid (a fixed delay races the WS / an HMR reload).
async function waitVMRow(page) {
	await page.waitForSelector('tbody tr', { timeout: 20000 });
}

// One context per theme: addInitScript re-runs on every navigation, so a
// live flip inside a context gets re-stamped; the flip tests cover that
// path, the sweeps pin the theme per context. Login gates on a real tree
// entry (burn-1) so the shot never races the WS inventory frame.
async function openThemed(browser, base, mode) {
	const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('${mode}'))`);
	const page = await ctx.newPage();
	await page.goto(base, { waitUntil: 'networkidle' });
	await page.locator('textarea').fill(token());
	await page.getByRole('button', { name: 'Sign in' }).click();
	await page.locator('aside').getByText('burn-1').waitFor({ timeout: 60000 });
	return { ctx, page };
}

const scenes = {
	icons: {
		desc: 'VM grid, detail header icons, Edit Settings modal (icons-*.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
			await login(page, DEV);
			await waitVMRow(page);
			await page.screenshot({ path: `${OUT}/icons-1-grid.png` });

			// Drill into a VM to show the detail header icons (Edit/Delete) + back-bar arrow.
			await page.locator('tbody tr').first().click();
			await page.waitForSelector('text=All VMs', { timeout: 5000 });
			await page.waitForTimeout(500);
			await page.screenshot({ path: `${OUT}/icons-2-detail.png` });

			// Open Edit Settings to show the modal close X + section chevrons.
			await page.getByRole('button', { name: /Edit Settings/ }).click();
			await page.waitForTimeout(500);
			await page.screenshot({ path: `${OUT}/icons-3-editmodal.png` });

			await browser.close();
			console.log('OK');
		},
	},

	bugfix: {
		desc: 'VM summary layout, snapshot-restore note, populated catalog (fix-*.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({
				viewport: { width: 1600, height: 950 },
				ignoreHTTPSErrors: true,
			});
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
			await page.locator('aside').getByText('vm-tenant-a', { exact: true }).click();
			await page.waitForSelector('img[alt="Console preview"]', { timeout: 15000 });
			await page.waitForTimeout(1500);
			await page.screenshot({ path: `${OUT}/fix-1-summary-layout.png` });
			console.log('summary layout: captured');

			// Snapshots tab: restore note + greyed restore.
			await page
				.locator('main')
				.getByRole('button', { name: /^snapshots$/i })
				.click();
			await page.waitForSelector('text=/Restore is disabled while the VM is running/', {
				timeout: 10000,
			});
			await page.waitForTimeout(800);
			await page.screenshot({ path: `${OUT}/fix-2-snapshot-note.png` });
			console.log('snapshot note: shown');

			// Catalog (dev backend kube:admin, so it is populated).
			await page.getByRole('button', { name: 'Catalog', exact: true }).click();
			await page.waitForSelector('aside >> text=fedora', { timeout: 10000 });
			await page.waitForTimeout(500);
			await page.screenshot({ path: `${OUT}/fix-3-catalog.png` });
			console.log('catalog: populated');

			await browser.close();
			console.log('OK');
		},
	},

	catalog: {
		desc: 'Catalog boot-image drawer + instance types (catalog-*.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
			await page.getByRole('button', { name: 'Catalog', exact: true }).click();
			await page.waitForSelector('text=Boot images');
			await page.waitForSelector('aside >> text=fedora');
			// Open a detail drawer, then hop to instance types.
			await page
				.locator('aside')
				.getByRole('button', { name: /^fedora\b/ })
				.first()
				.click();
			await page.waitForSelector('text=DataSource (CDI)');
			await page.screenshot({ path: `${OUT}/catalog-1-images.png` });
			await page.getByRole('button', { name: 'Instance types' }).click();
			await page.waitForSelector('text=u1.medium');
			await page.screenshot({ path: `${OUT}/catalog-2-instancetypes.png` });
			console.log('OK');
			await browser.close();
		},
	},

	deployed: {
		desc: 'Deployed app through the cluster route, dark grid (BASE overrides the target)',
		async run() {
			// Verify the DEPLOYED app (through the cluster route) carries the current UI.
			const base = process.env.BASE || 'https://dotvirt.apps.cluster-csqjp.dyn.redhatworkshops.io';
			const browser = await chromium.launch();
			const ctx = await browser.newContext({
				viewport: { width: 1440, height: 900 },
				ignoreHTTPSErrors: true,
			});
			await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('dark'))`);
			const page = await ctx.newPage();
			await page.goto(base, { waitUntil: 'networkidle' });
			await page.locator('textarea').fill(token());
			await page.getByRole('button', { name: 'Sign in' }).click();
			await page.locator('aside').getByText('burn-1').waitFor({ timeout: 60000 });
			await page.goto(`${base}/compute?tab=vms`, { waitUntil: 'networkidle' });
			await page.locator('tbody tr').first().waitFor({ timeout: 15000 });
			console.log('theme:', await page.evaluate(() => document.documentElement.dataset.theme));
			await page.screenshot({ path: `${OUT}/deployed-dark.png` });
			console.log('deployed-dark ok');
			await browser.close();
		},
	},

	lens: {
		desc: 'Networks/Storage lenses + wizard storage-class select (lens-*.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });

			await page.getByRole('button', { name: 'Networks', exact: true }).click();
			await page.waitForSelector('aside >> text=pod');
			// Scope to the pod network to show grid filtering + breadcrumb.
			await page.locator('aside').getByRole('button', { name: /^pod/ }).click();
			await page.getByRole('button', { name: 'VMs', exact: true }).click();
			await page.waitForTimeout(400);
			await page.screenshot({ path: `${OUT}/lens-1-networks.png` });
			console.log('networks lens ok');

			await page.getByRole('button', { name: 'Storage', exact: true }).click();
			await page.waitForTimeout(400);
			await page.screenshot({ path: `${OUT}/lens-2-storage.png` });
			console.log('storage lens ok');

			await page.getByRole('button', { name: 'New VM' }).click();
			// The wizard opens on "Name and project"; jump straight to the Storage step
			// (free rail navigation), where the storage-class select lives.
			await page
				.locator('div.fixed.inset-0.z-50')
				.getByRole('button', { name: /Storage/ })
				.click();
			await page.waitForSelector('text=Storage class');
			await page.waitForTimeout(300);
			await page.screenshot({ path: `${OUT}/lens-3-wizard.png` });
			console.log('wizard storage class ok');

			await browser.close();
			console.log('OK');
		},
	},

	net: {
		desc: 'Networking revisit: topology, segment/firewall/gateway modals (net-*.png, BASE_URL)',
		async run() {
			// Gates on the pinned Topology entry (proves the tree rendered) rather than
			// a VM row, since the landing tab is Summary.
			const base = process.env.BASE_URL ?? DEV;
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			const done = [];
			async function snap(name) {
				await page.screenshot({ path: `${OUT}/net-${name}.png` });
				done.push(name);
			}
			async function step(name, fn) {
				try {
					await fn();
					await page.waitForTimeout(600);
					await snap(name);
				} catch (e) {
					console.log(`STEP ${name} FAILED: ${e.message}`);
				}
			}
			async function closeModal() {
				try {
					await page.getByRole('button', { name: 'Cancel' }).click({ timeout: 2000 });
				} catch {}
				try {
					await page
						.locator('div[role="presentation"]')
						.waitFor({ state: 'detached', timeout: 3000 });
				} catch {}
			}

			await login(page, base);

			// Tree ready when the pinned Topology entry has painted.
			await page.getByRole('button', { name: 'Topology' }).waitFor({ timeout: 25000 });
			await page.waitForTimeout(800);
			await snap('0-landing');

			// The VM grid (VMs tab): baseline.
			await step('1-grid', async () => {
				await page.getByRole('button', { name: 'VMs', exact: true }).first().click();
				await page.waitForSelector('tbody tr', { timeout: 8000 });
			});

			await step('2-topology', async () => {
				await page.getByRole('button', { name: 'Topology' }).click();
			});

			// New Segment modal (toggle topology off first so the header is clean).
			await step('3-new-segment', async () => {
				await page.getByRole('button', { name: 'Topology' }).click();
				await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
				await page.getByRole('button', { name: 'New Segment' }).click();
			});
			await closeModal();

			// Tier-1 egress firewall modal (right-click a repo-backed project).
			await step('4-egress-firewall', async () => {
				await page
					.locator('aside')
					.getByText('team-a', { exact: true })
					.first()
					.click({ button: 'right' });
				await page.getByRole('button', { name: /New Egress Firewall/ }).click();
			});
			await closeModal();

			// Dual-vocab segment Configure panel: Segments lens, a group, Configure.
			await step('5-segment-configure', async () => {
				await page.getByRole('button', { name: 'Segments' }).click();
				await page.waitForTimeout(500);
				await page
					.locator('aside button.font-semibold, aside .font-semibold')
					.filter({ hasText: /net|VM Network|udn|frontend|backend/i })
					.first()
					.click();
				await page.getByRole('button', { name: 'Configure' }).click();
			});

			// Tier-0 service modal (New menu).
			await step('6-tier0', async () => {
				await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
				await page.getByRole('button', { name: 'New Tier-0 Service' }).click();
			});
			await closeModal();

			// Distributed Firewall modal (right-click a project).
			await step('7-dfw', async () => {
				await page.getByRole('button', { name: 'Projects' }).click();
				await page.waitForTimeout(300);
				await page
					.locator('aside')
					.getByText('team-a', { exact: true })
					.first()
					.click({ button: 'right' });
				await page.getByRole('button', { name: /New Security Policy/ }).click();
			});
			await closeModal();

			// Admin (cluster-wide) firewall modal (New menu).
			await step('8-admin-fw', async () => {
				await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
				await page.getByRole('button', { name: 'New Admin Firewall' }).click();
			});
			await closeModal();

			console.log('shots captured:', done.join(', '));
			// Diagnostics: list header + lens buttons so selectors can be fixed if a step missed.
			const labels = await page.locator('header button, aside button').allInnerTexts();
			console.log(
				'buttons:',
				labels
					.map((s) => s.replace(/\s+/g, ' ').trim())
					.filter(Boolean)
					.slice(0, 25)
					.join(' | '),
			);
			await browser.close();
		},
	},

	network: {
		desc: 'VM adapters, networks lens, network detail, node fabric (net-N-*.png, BASE_URL)',
		async run() {
			const base = process.env.BASE_URL || DEV;
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			page.setDefaultTimeout(30000);

			async function step(name, fn) {
				try {
					await fn();
					console.log('OK   ' + name);
				} catch (e) {
					console.log('FAIL ' + name + ' :: ' + (e.message || e).split('\n')[0]);
				}
			}

			await login(page, base);
			await page.getByRole('button', { name: /New VM/ }).waitFor();
			// The port-group catalog loads async after login; wait for it so resolveNIC has
			// data (else the first VM's adapter renders the raw "Pod network" fallback).
			await page
				.waitForResponse((r) => r.url().endsWith('/api/networks'), { timeout: 15000 })
				.catch(() => {});
			await page.waitForTimeout(300);

			await step('vm-adapters', async () => {
				await page.getByRole('button', { name: 'VMs', exact: true }).click();
				const row = page.locator('tbody tr').first();
				await row.waitFor();
				await row.click();
				await page.getByRole('button', { name: /Edit Settings/ }).waitFor();
				// VM-detail tabs render lowercase text via `capitalize` CSS, so the accessible
				// name is lowercase; match case-insensitively.
				await page.getByRole('button', { name: /^configure$/i }).click();
				await page.getByRole('button', { name: 'Network', exact: true }).click();
				await page.waitForSelector('text=Network adapters');
				await page.screenshot({ path: `${OUT}/net-1-vm-adapters.png` });
			});

			await step('networks-lens', async () => {
				await page.getByRole('button', { name: 'Networks', exact: true }).click();
				await page.waitForSelector('text=network-a');
				await page.screenshot({ path: `${OUT}/net-2-networks-lens.png` });
			});

			await step('network-detail', async () => {
				await page.getByText('network-a', { exact: true }).first().click();
				await page.getByRole('button', { name: 'Configure', exact: true }).click();
				await page.waitForSelector('text=Backing');
				await page.screenshot({ path: `${OUT}/net-3-network-detail.png` });
			});

			await step('node-fabric', async () => {
				await page.getByRole('button', { name: 'Nodes', exact: true }).click();
				await page.getByText('hetznet', { exact: true }).first().click();
				await page.getByRole('button', { name: 'Configure', exact: true }).click();
				await page.waitForSelector('text=Physical adapters');
				await page.screenshot({ path: `${OUT}/net-4-node-fabric.png` });
			});

			await browser.close();
			console.log('done');
		},
	},

	perf: {
		desc: 'VM performance charts + Month range (perf-*.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });

			await page.getByRole('button', { name: 'VMs', exact: true }).click();
			await page.locator('tbody tr', { hasText: 'vm-tenant-a' }).first().click();
			await page.locator('main').getByRole('button', { name: 'monitor' }).click();
			await page.locator('main').getByRole('button', { name: 'performance' }).click();
			await page.waitForSelector('.uplot', { timeout: 30000 });
			await page.waitForSelector('text=Disk IOPS');
			await page.waitForTimeout(1200);
			await page.screenshot({ path: `${OUT}/perf-1-vm-charts.png`, fullPage: true });
			console.log('vm charts ok');

			// Month range renders (retention-bounded data is fine).
			await page.getByRole('button', { name: 'Month', exact: true }).click();
			await page.waitForTimeout(2500);
			await page.screenshot({ path: `${OUT}/perf-2-month.png`, fullPage: true });
			console.log('month range ok');

			await browser.close();
			console.log('OK');
		},
	},

	'perf-theme': {
		desc: 'Perf charts in light, live flip to dark, fresh dark (perf-{light,flipped-dark,dark}.png)',
		async run() {
			// Real paint gate: wait until the first chart canvas has a meaningful
			// number of painted pixels, then shoot.
			const browser = await chromium.launch();

			async function openPerf(page) {
				await page.goto(`${DEV}/vm/drs-lab/burn-1?tab=monitor`, { waitUntil: 'networkidle' });
				await page.locator('main').getByText('Performance').first().click();
				await page.locator('.uplot-host canvas').first().waitFor({ timeout: 30000 });
			}

			async function waitPainted(page) {
				await page.waitForFunction(
					() => {
						const c = document.querySelector('.uplot-host canvas');
						if (!c) return false;
						const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
						let n = 0;
						for (let i = 3; i < d.length; i += 4) if (d[i] !== 0) n++;
						return n > 2000;
					},
					{ timeout: 30000 },
				);
				await page.waitForTimeout(300);
			}

			const light = await openThemed(browser, DEV, 'light');
			await openPerf(light.page);
			await waitPainted(light.page);
			await light.page.screenshot({ path: `${OUT}/perf-light.png` });
			console.log('perf-light ok');

			// Live flip on the loaded charts: the rebuild must repaint with dark colors.
			await light.page.locator('header').getByRole('button', { name: /admin/ }).click();
			await light.page.getByRole('button', { name: 'Dark' }).click();
			await light.page.keyboard.press('Escape');
			await waitPainted(light.page);
			const mode = await light.page.evaluate(() => document.documentElement.dataset.theme);
			if (mode !== 'dark') throw new Error(`expected dark, got ${mode}`);
			await light.page.screenshot({ path: `${OUT}/perf-flipped-dark.png` });
			console.log('perf-flipped-dark ok');
			await light.ctx.close();

			const dark = await openThemed(browser, DEV, 'dark');
			await openPerf(dark.page);
			await waitPainted(dark.page);
			await dark.page.screenshot({ path: `${OUT}/perf-dark.png` });
			console.log('perf-dark ok');
			await browser.close();
		},
	},

	perms: {
		desc: 'New-menu capability gating; run per token with LABEL env (perms-<label>.png)',
		async run() {
			// Run once with an admin token and once with a restricted (namespace-only)
			// token and compare.
			const LABEL = process.env.LABEL || 'x';
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			await login(page, DEV);
			await page.getByRole('button', { name: 'Topology' }).waitFor({ timeout: 25000 });
			await page.waitForTimeout(700);
			await page.locator('header').getByRole('button', { name: 'New', exact: true }).click();
			await page.waitForTimeout(400);
			await page.screenshot({ path: `${OUT}/perms-${LABEL}.png` });
			console.log('ok', LABEL);
			await browser.close();
		},
	},

	phase3: {
		desc: 'Scope perf sub-rail + clone/NotTracked/adopt flow (phase3-*.png, CLONE_TARGET)',
		async run() {
			const SRC = 'vm-tenant-a';
			const TARGET = process.env.CLONE_TARGET || 'vm-tenant-a-ui';
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			// Gate on streamed inventory, not a fixed sleep (see the icons scene).
			await page.waitForSelector(`text=${SRC}`, { timeout: 20000 });

			// Container Monitor: Performance sub-rail.
			await page.getByRole('button', { name: 'Monitor', exact: true }).click();
			await page.getByRole('button', { name: 'performance' }).click();
			await page.waitForSelector('.uplot', { timeout: 30000 });
			await page.waitForTimeout(1000); // legends settle
			await page.screenshot({ path: `${OUT}/phase3-1-scope-performance.png` });
			console.log('scope performance: charts rendered');

			// Open the source VM and clone it.
			await page.getByRole('button', { name: 'VMs', exact: true }).click();
			await page.locator('tbody tr', { hasText: SRC }).first().click();
			await page.waitForSelector('text=All VMs');
			await page.getByRole('button', { name: /^Actions/ }).click();
			await page.getByRole('button', { name: 'Clone…' }).click();
			await page.waitForSelector('#clone-target-input');
			await page.fill('#clone-target-input', TARGET);
			await page.screenshot({ path: `${OUT}/phase3-2-clone-modal.png` });
			await page.getByRole('button', { name: 'Clone', exact: true }).click();
			// The modal polls the clone list; wait for the phase to settle.
			await page.waitForSelector('text=Succeeded', { timeout: 180000 });
			await page.screenshot({ path: `${OUT}/phase3-3-clone-succeeded.png` });
			console.log('clone: succeeded');
			// The footer Close (the header X also has the accessible name "Close").
			await page.locator('footer').getByRole('button', { name: 'Close' }).click();

			// The target lands NotTracked; open it.
			await page.getByRole('button', { name: 'All VMs' }).first().click();
			await page.getByRole('button', { name: 'VMs', exact: true }).click();
			// The new VM arrives over the live stream.
			await page.waitForSelector(`tbody tr:has-text("${TARGET}")`, { timeout: 60000 });
			await page.locator('tbody tr', { hasText: TARGET }).first().click();
			await page.waitForSelector('text=Not in git', { timeout: 15000 });
			await page.screenshot({ path: `${OUT}/phase3-4-nottracked-banner.png` });
			console.log('target: NotTracked banner shown');

			// Adopt into git (retry while the running-branch export catches up).
			for (let attempt = 1; ; attempt++) {
				await page.getByRole('button', { name: 'Adopt into git' }).click();
				try {
					await page.waitForSelector('text=staged into Changes', { timeout: 8000 });
					break;
				} catch {
					if (attempt >= 8) throw new Error('adopt never succeeded');
					await page.waitForTimeout(12000); // export tick (30s) + git poll (10s)
				}
			}
			await page.screenshot({ path: `${OUT}/phase3-5-adopted.png` });
			console.log('adopt: staged into Changes');

			// The Changes badge should now show the staged create.
			await page.getByRole('button', { name: /^Changes/ }).click();
			await page.waitForSelector(`text=${TARGET}`);
			await page.waitForTimeout(500);
			await page.screenshot({ path: `${OUT}/phase3-6-changes-panel.png` });
			console.log('changes panel: create item present');

			await browser.close();
			console.log('OK');
		},
	},

	phase5: {
		desc: 'Overcommit chips, node maintenance panel, console preview (phase5-*.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 950 } });
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });

			// Overcommit chips on the All-VMs Summary.
			await page.waitForSelector('text=Overcommit', { timeout: 15000 });
			await page.waitForTimeout(800);
			await page.screenshot({ path: `${OUT}/phase5-1-overcommit.png` });
			console.log('overcommit chips: shown');

			// Nodes lens, a node, Configure, Maintenance panel.
			await page.getByRole('button', { name: 'Nodes', exact: true }).click();
			await page.locator('aside').getByText('hetznet', { exact: true }).click();
			await page.getByRole('button', { name: 'Configure', exact: true }).click();
			await page.waitForSelector('text=Maintenance', { timeout: 10000 });
			await page.waitForTimeout(500);
			await page.screenshot({ path: `${OUT}/phase5-2-node-maintenance.png` });
			console.log('node maintenance panel: shown');

			// A running VM's Summary: console preview thumbnail.
			await page.getByRole('button', { name: 'Projects', exact: true }).click();
			await page.locator('aside').getByText('vm-tenant-a', { exact: true }).click();
			await page.waitForSelector('text=Capacity', { timeout: 10000 });
			// The screenshot img loads async; wait for it.
			await page.waitForSelector('img[alt="Console preview"]', { timeout: 15000 });
			await page.waitForTimeout(1500);
			await page.screenshot({ path: `${OUT}/phase5-3-console-preview.png` });
			console.log('console preview: rendered');

			await browser.close();
			console.log('OK');
		},
	},

	'quota-alarm': {
		desc: 'Project quota note + empty Alarms dock (quota-alarms.png)',
		async run() {
			const browser = await chromium.launch();
			const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
			// Project scope, then Configure (quota note under the project card).
			await page
				.locator('aside')
				.getByRole('button', { name: /team-a/ })
				.click();
			await page.getByRole('button', { name: 'Configure', exact: true }).click();
			await page.waitForSelector('text=No ResourceQuotas');
			// Dock ALARMS tab.
			await page.getByRole('button', { name: /^Alarms/ }).click();
			await page.waitForSelector('text=No triggered alarms');
			await page.waitForTimeout(400);
			await page.screenshot({ path: `${OUT}/quota-alarms.png` });
			console.log('OK');
			await browser.close();
		},
	},

	theme: {
		desc: 'Login screen light + dark against vite preview, no backend (login-*.png, BASE)',
		async run() {
			// No backend: /api/me fails so Login renders. Verifies the dark override
			// block, the FOUC stamp, and basic token readability.
			const base = process.env.BASE || 'http://localhost:4173';
			const browser = await chromium.launch();
			for (const mode of ['light', 'dark']) {
				const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
				await ctx.addInitScript(`localStorage.setItem('dotvirt.theme', JSON.stringify('${mode}'))`);
				const page = await ctx.newPage();
				await page.goto(base, { waitUntil: 'networkidle' });
				await page.waitForTimeout(500);
				const stamped = await page.evaluate(() => document.documentElement.dataset.theme);
				const bodyBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
				console.log(`${mode}: data-theme=${stamped} body-bg=${bodyBg}`);
				await page.screenshot({ path: `${OUT}/login-${mode}.png` });
				await ctx.close();
			}
			await browser.close();
		},
	},

	upload: {
		desc: 'Image upload form + live progress; needs /tmp/test-upload.img (upload-*.png)',
		async run() {
			const browser = await chromium.launch({ args: ['--ignore-certificate-errors'] });
			const page = await browser.newPage({
				viewport: { width: 1440, height: 950 },
				ignoreHTTPSErrors: true,
			});
			page.setDefaultTimeout(30000);
			await login(page, DEV);
			await page.waitForSelector('text=vm-tenant-a', { timeout: 20000 });
			await page.getByRole('button', { name: 'Upload', exact: true }).click();
			await page.waitForSelector('text=Upload image');
			await page.setInputFiles('input[type=file]', '/tmp/test-upload.img');
			await page.fill('input[placeholder="my-image"]', 'ui-upload-test');
			await page.waitForTimeout(400);
			await page.screenshot({ path: `${OUT}/upload-1-form.png` });
			console.log('form: filled');
			// Drive the real upload (browser to proxy, cross-origin).
			await page.getByRole('button', { name: 'Upload', exact: true }).last().click();
			// Wait for Importing or Done (proves create, ready, stream, import).
			await page.waitForSelector('text=/Importing|is ready/', { timeout: 120000 });
			await page.waitForTimeout(500);
			await page.screenshot({ path: `${OUT}/upload-2-progress.png` });
			await page.waitForSelector('text=is ready', { timeout: 120000 });
			await page.waitForTimeout(300);
			await page.screenshot({ path: `${OUT}/upload-3-done.png` });
			console.log('upload: done');
			await browser.close();
			console.log('OK');
		},
	},

	walkthrough: {
		desc: 'Full section sweep in light, live theme flip, sweep again in dark (wt-*.png)',
		async run() {
			const browser = await chromium.launch();
			const failures = [];

			async function shot(page, name) {
				const mode = await page.evaluate(() => document.documentElement.dataset.theme);
				await page.screenshot({ path: `${OUT}/wt-${name}-${mode}.png` });
				console.log(`shot wt-${name}-${mode}`);
			}

			async function step(name, fn) {
				try {
					await fn();
				} catch (e) {
					failures.push(`${name}: ${String(e).split('\n')[0]}`);
					console.log(`FAIL ${name}: ${String(e).split('\n')[0]}`);
				}
			}

			async function sweep(page) {
				await step('compute-grid', async () => {
					await page.goto(`${DEV}/compute?tab=vms`, { waitUntil: 'networkidle' });
					await page.locator('tbody tr').first().waitFor({ timeout: 15000 });
					await shot(page, 'compute-vms');
				});
				await step('compute-summary', async () => {
					await page.goto(`${DEV}/compute?tab=summary`, { waitUntil: 'networkidle' });
					await page.waitForTimeout(2500); // rings/quotas fetch
					await shot(page, 'compute-summary');
				});
				await step('vm-summary', async () => {
					await page.goto(`${DEV}/vm/drs-lab/burn-1?tab=summary`, { waitUntil: 'networkidle' });
					await page.getByText('Capacity', { exact: false }).first().waitFor({ timeout: 15000 });
					await page.waitForTimeout(2000);
					await shot(page, 'vm-summary');
				});
				await step('vm-monitor-perf', async () => {
					await page.goto(`${DEV}/vm/drs-lab/burn-1?tab=monitor`, { waitUntil: 'networkidle' });
					await page.locator('main').getByText('Performance').first().click();
					await page.locator('.uplot-host canvas').first().waitFor({ timeout: 20000 });
					await page.waitForTimeout(1500);
					await shot(page, 'vm-perf');
				});
				await step('vm-configure', async () => {
					await page.goto(`${DEV}/vm/drs-lab/burn-1?tab=configure`, { waitUntil: 'networkidle' });
					await page.waitForTimeout(1000);
					await shot(page, 'vm-configure');
				});
				await step('hosts', async () => {
					await page.goto(`${DEV}/hosts`, { waitUntil: 'networkidle' });
					await page.waitForTimeout(2000);
					await shot(page, 'hosts');
				});
				await step('networking', async () => {
					await page.goto(`${DEV}/networking`, { waitUntil: 'networkidle' });
					await page.waitForTimeout(1500);
					await shot(page, 'networking');
				});
				await step('storage', async () => {
					await page.goto(`${DEV}/storage`, { waitUntil: 'networkidle' });
					await page.waitForTimeout(1500);
					await shot(page, 'storage');
				});
				await step('catalog', async () => {
					await page.goto(`${DEV}/catalog`, { waitUntil: 'networkidle' });
					await page.waitForTimeout(1500);
					await shot(page, 'catalog');
				});
				await step('dock-alarms', async () => {
					await page.goto(`${DEV}/compute?tab=vms`, { waitUntil: 'networkidle' });
					await page.locator('tbody tr').first().waitFor({ timeout: 15000 });
					await page.getByRole('button', { name: 'Alarms', exact: false }).click();
					await page.waitForTimeout(1000);
					await shot(page, 'dock-alarms');
				});
				await step('clone-modal', async () => {
					await page.goto(`${DEV}/vm/drs-lab/burn-1?tab=summary`, { waitUntil: 'networkidle' });
					await page.getByRole('button', { name: 'Actions' }).click();
					await page.getByRole('button', { name: /Clone…/ }).click();
					await page.locator('[role="dialog"]').waitFor({ timeout: 5000 });
					await shot(page, 'clone-modal');
					await page.keyboard.press('Escape');
				});
			}

			const light = await openThemed(browser, DEV, 'light');
			await sweep(light.page);

			// Flip to dark LIVE through the user menu (exercises store + synchronous
			// stamp + uPlot rebuild), starting from the perf charts so the flip is
			// visible on canvas.
			await step('theme-flip', async () => {
				const page = light.page;
				await page.goto(`${DEV}/vm/drs-lab/burn-1?tab=monitor`, { waitUntil: 'networkidle' });
				await page.locator('main').getByText('Performance').first().click();
				await page.locator('.uplot-host canvas').first().waitFor({ timeout: 20000 });
				await page.locator('header').getByRole('button', { name: /admin/ }).click();
				await page.getByRole('button', { name: 'Dark' }).click();
				await page.keyboard.press('Escape');
				await page.waitForTimeout(1200);
				const mode = await page.evaluate(() => document.documentElement.dataset.theme);
				if (mode !== 'dark') throw new Error(`data-theme=${mode} after toggle`);
				await shot(page, 'vm-perf-flipped');
			});
			await light.ctx.close();

			const dark = await openThemed(browser, DEV, 'dark');
			await sweep(dark.page);

			console.log(failures.length ? `FAILURES:\n${failures.join('\n')}` : 'ALL STEPS OK');
			await browser.close();
			process.exit(failures.length ? 1 : 0);
		},
	},
};

const arg = process.argv[2];
if (!arg || arg === '--list') {
	const w = Math.max(...Object.keys(scenes).map((k) => k.length));
	for (const [name, s] of Object.entries(scenes)) {
		console.log(`${name.padEnd(w)}  ${s.desc}`);
	}
	process.exit(arg ? 0 : 1);
}
const scene = scenes[arg];
if (!scene) {
	console.error(`unknown scene "${arg}"; run: node shot.mjs --list`);
	process.exit(1);
}
await scene.run();
