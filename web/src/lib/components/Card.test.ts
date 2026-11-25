// @vitest-environment jsdom
import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';

import type { Item } from '#lib/api';

import Card from './Card.svelte';

// Issue #49: a card with a cover shows it, a card without keeps the placeholder.
// The cover is same-origin, so the CSP does not move: img-src stays 'self'.

function item(overrides: Partial<Item> = {}): Item {
	return {
		id: 'item-1',
		url: 'https://example.test/a',
		urlOriginal: 'https://example.test/a',
		title: 'A page',
		note: '',
		summary: '',
		kind: 'article',
		kindSource: 'rule',
		source: 'app',
		state: 'enriched',
		createdAt: '2026-11-24T12:00:00Z',
		savedAt: '2026-11-24T12:00:00Z',
		updatedAt: '2026-11-24T12:00:00Z',
		...overrides
	};
}

describe('Card', () => {
	it('shows the proxied page image when the item has one', () => {
		const { container } = render(Card, { item: item({ cover: true }) });

		const image = container.querySelector('img.coverimg');
		expect(image).not.toBeNull();
		expect(image?.getAttribute('src')).toBe('/api/items/item-1/cover');
		// Same-origin, and the attributes the issue asks for: lazy, decoded off
		// the main thread, and explicit dimensions so a late load does not reflow.
		expect(image?.getAttribute('width')).toBe('640');
		expect(image?.getAttribute('height')).toBe('400');
		expect(image?.getAttribute('loading')).toBe('lazy');
		expect(image?.getAttribute('decoding')).toBe('async');

		// The gradient stays behind the image; the glyph is not drawn over it.
		expect(container.querySelector('.kindmark')).toBeNull();
	});

	it('keeps the placeholder for an item with no cover', () => {
		const { container } = render(Card, { item: item() });

		expect(container.querySelector('img.coverimg')).toBeNull();
		expect(container.querySelector('.kindmark')).not.toBeNull();
	});

	it('falls back to the placeholder when the image fails to load', async () => {
		const { container } = render(Card, { item: item({ cover: true }) });
		const image = container.querySelector('img.coverimg');
		expect(image).not.toBeNull();

		image?.dispatchEvent(new Event('error'));
		await Promise.resolve();

		// The card is still a card: no broken-image box, the glyph returns.
		expect(container.querySelector('.cover')).not.toBeNull();
		expect(container.querySelector('img.coverimg')).toBeNull();
		expect(container.querySelector('.kindmark')).not.toBeNull();
	});
});
