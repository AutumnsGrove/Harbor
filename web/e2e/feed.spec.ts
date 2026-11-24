import { expect, test, type Page } from '@playwright/test';

// #51's acceptance item, finally in CI (#58): the feed must read left to right,
// top to bottom, and the assertion is geometry rather than the intent behind it.
// jsdom cannot do this — every rect is zero there — so the browser does.

type Fixture = { id: string; title: string; kind?: string };

const ITEMS: Fixture[] = Array.from({ length: 16 }, (_, index) => ({
	id: `item-${index + 1}`,
	title: `Item ${index + 1}`
}));

const PENDING: Fixture[] = [
	{ id: 'pending-1', title: 'Pending 1' },
	{ id: 'pending-2', title: 'Pending 2' }
];

function itemBody(fixture: Fixture) {
	const now = new Date('2026-11-24T12:00:00Z').toISOString();
	return {
		id: fixture.id,
		url: `https://example.test/${fixture.id}`,
		urlOriginal: `https://example.test/${fixture.id}`,
		title: fixture.title,
		note: '',
		summary: '',
		kind: fixture.kind ?? 'article',
		kindSource: 'rule',
		source: 'app',
		state: 'enriched',
		createdAt: now,
		savedAt: now,
		updatedAt: now
	};
}

/**
 * Stub the three endpoints the home page reads. Nothing reaches the Go binary,
 * so the test owns the data and the order is unambiguous.
 */
async function mockApi(
	page: Page,
	{ items = ITEMS.length, pageSize = 8, pending = 0 } = {}
) {
	const shown = ITEMS.slice(0, items);

	await page.route('**/api/items**', async (route) => {
		const url = new URL(route.request().url());
		const cursor = url.searchParams.get('cursor');

		// "Load more" asks for the second page; anything else is the first.
		const body = cursor
			? { items: shown.slice(pageSize).map(itemBody), next: null }
			: {
					items: shown.slice(0, pageSize).map(itemBody),
					next: shown.length > pageSize ? 'cursor-2' : null
				};

		await route.fulfill({ json: body });
	});

	await page.route('**/api/pending', (route) =>
		route.fulfill({
			json: {
				jobs: PENDING.slice(0, pending).map((job) => ({
					id: job.id,
					title: job.title,
					url: `https://example.test/${job.id}`,
					source: 'worker',
					kind: 'article',
					status: 'queued',
					createdAt: new Date('2026-11-24T12:00:00Z').toISOString()
				}))
			}
		})
	);

	await page.route('**/api/attention', (route) => route.fulfill({ json: { items: [] } }));
}

/** Every card, sorted the way an eye reads the grid. */
async function visualOrder(page: Page): Promise<string[]> {
	const boxes = await page.locator('.feed .card').evaluateAll((nodes) =>
		nodes.map((node) => {
			const rect = node.getBoundingClientRect();
			// The label is the item's title; the pending cards say "— saving".
			const label = node.getAttribute('aria-label') ?? node.querySelector('h3')?.textContent ?? '';
			return { title: label.replace(' — saving', '').trim(), top: rect.top, left: rect.left };
		})
	);

	return boxes
		.sort((a, b) => (Math.abs(a.top - b.top) > 1 ? a.top - b.top : a.left - b.left))
		.map((box) => box.title);
}

const WIDTHS: Array<{ width: number; columns: number }> = [
	{ width: 1280, columns: 4 },
	{ width: 900, columns: 3 },
	{ width: 600, columns: 2 },
	{ width: 320, columns: 1 }
];

for (const { width, columns } of WIDTHS) {
	test(`at ${width}px the grid has ${columns} columns and reads left to right`, async ({ page }) => {
		await page.setViewportSize({ width, height: 900 });
		await mockApi(page);
		await page.goto('/');
		await expect(page.locator('.feed .card')).toHaveCount(8);

		// One .col per column, which is the count --cols resolved to.
		await expect(page.locator('.feed .col')).toHaveCount(columns);

		// The assertion from #51: sorted by top then left, the first row is
		// saves 1..N in save order.
		const order = await visualOrder(page);
		expect(order.slice(0, columns)).toEqual(ITEMS.slice(0, columns).map((item) => item.title));

		// And the whole grid is exactly the API order, row by row.
		expect(order).toEqual(ITEMS.slice(0, 8).map((item) => item.title));

		// Nothing scrolls sideways, at any width.
		const overflow = await page.evaluate(
			() => document.documentElement.scrollWidth - document.documentElement.clientWidth
		);
		expect(overflow).toBeLessThanOrEqual(0);
	});
}

test('a resize re-flows instead of leaving a stale column count', async ({ page }) => {
	await page.setViewportSize({ width: 1280, height: 900 });
	await mockApi(page);
	await page.goto('/');
	await expect(page.locator('.feed .col')).toHaveCount(4);

	for (const { width, columns } of WIDTHS.slice(1)) {
		await page.setViewportSize({ width, height: 900 });
		await expect(page.locator('.feed .col')).toHaveCount(columns);
		// The top row still reads 1..N after the re-flow.
		const order = await visualOrder(page);
		expect(order.slice(0, columns)).toEqual(ITEMS.slice(0, columns).map((item) => item.title));
	}
});

test('pending saves stay ahead of the library in the same order', async ({ page }) => {
	await page.setViewportSize({ width: 1280, height: 900 });
	await mockApi(page, { pending: 2 });
	await page.goto('/');
	await expect(page.locator('.feed .card')).toHaveCount(10);

	// Pending cards are not links, so label them by their own aria-label.
	const order = await visualOrder(page);
	expect(order.slice(0, 4)).toEqual(['Pending 1', 'Pending 2', 'Item 1', 'Item 2']);
});

test('load more appends without moving a card already on screen', async ({ page }) => {
	await page.setViewportSize({ width: 1280, height: 900 });
	await mockApi(page);
	await page.goto('/');
	await expect(page.locator('.feed .card')).toHaveCount(8);

	const before = await page.locator('.feed .card h3').evaluateAll((nodes) =>
		nodes.map((node) => {
			const rect = node.getBoundingClientRect();
			return { title: node.textContent?.trim() ?? '', top: Math.round(rect.top), left: Math.round(rect.left) };
		})
	);

	await page.getByRole('button', { name: 'Load more' }).click();
	await expect(page.locator('.feed .card')).toHaveCount(16);

	const after = await page.locator('.feed .card h3').evaluateAll((nodes) =>
		nodes.map((node) => {
			const rect = node.getBoundingClientRect();
			return { title: node.textContent?.trim() ?? '', top: Math.round(rect.top), left: Math.round(rect.left) };
		})
	);

	for (const card of before) {
		expect(after).toContainEqual(card);
	}
});
