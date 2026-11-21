import { describe, expect, it } from 'vitest';

import {
	addressLines,
	availabilityLabel,
	dateLabel,
	facts,
	hasPanel,
	humanDuration,
	kindFacts,
	panelKind,
	priceLabel,
	ratingLabel
} from './details';

// The per-kind layouts show what details_json says, formatted. Each helper has
// to survive the shapes JSON-LD actually ships: ISO durations, enums, prices as
// strings, and pages that skip a field entirely.

describe('humanDuration', () => {
	it('reads ISO 8601 durations', () => {
		expect(humanDuration('PT25M')).toBe('25 min');
		expect(humanDuration('PT1H25M')).toBe('1 hr 25 min');
		expect(humanDuration('PT45S')).toBe('45 sec');
		expect(humanDuration('P1DT2H')).toBe('1 day 2 hr');
		expect(humanDuration('P1W')).toBe('7 days');
	});

	it('drops seconds once there is an hour to report', () => {
		expect(humanDuration('PT1H3M12S')).toBe('1 hr 3 min');
	});

	it('reads a clock and leaves free text alone', () => {
		expect(humanDuration('1:25:00')).toBe('1 hr 25 min');
		expect(humanDuration('12:30')).toBe('12 min');
		expect(humanDuration('overnight')).toBe('overnight');
	});

	it('answers nothing for missing or zero-length input', () => {
		expect(humanDuration(undefined)).toBe('');
		expect(humanDuration('  ')).toBe('');
		expect(humanDuration('PT0S')).toBe('');
		expect(humanDuration('0:00')).toBe('');
	});
});

describe('priceLabel', () => {
	const formatted = (amount: number, currency: string) =>
		new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount);

	it('formats a declared currency', () => {
		expect(priceLabel({ price: '12.50', currency: 'USD' })).toBe(formatted(12.5, 'USD'));
	});

	it('reads a decimal comma instead of inventing thousands', () => {
		// JSON-LD keeps the price as text, so 12,50 is twelve fifty, not 1250.
		expect(priceLabel({ price: '12,50', currency: 'EUR' })).toBe(formatted(12.5, 'EUR'));
		expect(priceLabel({ price: '1.299,00', currency: 'EUR' })).toBe(formatted(1299, 'EUR'));
	});

	it('reads grouping separators', () => {
		expect(priceLabel({ price: '1,299.00', currency: 'USD' })).toBe(formatted(1299, 'USD'));
		expect(priceLabel({ price: '1,299', currency: 'USD' })).toBe(formatted(1299, 'USD'));
	});

	it('keeps a price it cannot read, and the currency', () => {
		expect(priceLabel({ price: 'about 20', currency: 'USD' })).toBe('about 20 USD');
		expect(priceLabel({ price: '12.50', currency: 'XYZQ' })).toBe('12.50 XYZQ');
		expect(priceLabel({ price: '12.50' })).toBe('12.50');
	});

	it('answers nothing without a price', () => {
		expect(priceLabel(undefined)).toBe('');
		expect(priceLabel({ currency: 'USD' })).toBe('');
	});
});

describe('availabilityLabel', () => {
	it('turns the schema.org enum into words', () => {
		expect(availabilityLabel('InStock')).toBe('In stock');
		expect(availabilityLabel('OutOfStock')).toBe('Out of stock');
		expect(availabilityLabel('PreOrder')).toBe('Pre-order');
	});

	it('humanises an enum the map has not met', () => {
		expect(availabilityLabel('MadeToOrder')).toBe('Made to order');
		expect(availabilityLabel(undefined)).toBe('');
	});
});

describe('ratingLabel', () => {
	it('renders a value with its scale', () => {
		expect(ratingLabel({ value: 4.6 })).toBe('4.6 of 5');
		expect(ratingLabel({ value: 4, best: 10 })).toBe('4 of 10');
	});

	it('adds a count when the page gave one', () => {
		expect(ratingLabel({ value: 4.6, count: 1204 })).toContain('ratings');
		expect(ratingLabel({ value: 4.6, count: 1204 })).toContain('4.6 of 5');
	});

	it('answers nothing without a value', () => {
		expect(ratingLabel(undefined)).toBe('');
		expect(ratingLabel({ count: 4 })).toBe('');
	});
});

describe('addressLines', () => {
	it('prints street, locality and country in postal order', () => {
		expect(
			addressLines({
				streetAddress: '12 Main St',
				addressLocality: 'Springfield',
				addressRegion: 'IL',
				postalCode: '62701',
				addressCountry: 'US'
			})
		).toEqual(['12 Main St', 'Springfield, IL 62701', 'US']);
	});

	it('skips the lines the page did not give', () => {
		expect(addressLines({ addressLocality: 'Lisbon', addressCountry: 'PT' })).toEqual([
			'Lisbon',
			'PT'
		]);
		expect(addressLines(undefined)).toEqual([]);
	});
});

describe('dateLabel', () => {
	it('keeps a bare year', () => {
		expect(dateLabel('2024')).toBe('2024');
	});

	it('formats an ISO date without the time', () => {
		const shown = dateLabel('2024-03-02T10:00:00Z');
		expect(shown).not.toBe('');
		expect(shown).not.toContain('T');
		expect(shown).toContain('2024');
	});

	it('treats a date-only string as a calendar day, not a local instant', () => {
		const utc = new Date(Date.UTC(2024, 8, 12)).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
			timeZone: 'UTC'
		});
		expect(dateLabel('2024-09-12')).toBe(utc);
	});

	it('returns unparseable text unchanged', () => {
		expect(dateLabel('sometime in spring')).toBe('sometime in spring');
		expect(dateLabel(undefined)).toBe('');
	});
});

describe('facts', () => {
	it('drops rows with no value so an empty state is a missing row', () => {
		expect(
			facts([
				{ label: 'Author', value: 'Jo' },
				{ label: 'Section', value: '  ' }
			])
		).toEqual([{ label: 'Author', value: 'Jo' }]);
	});
});

describe('panelKind', () => {
	it('lets a specific JSON-LD type win over the article fallback', () => {
		expect(panelKind('article', 'Recipe')).toBe('recipe');
		expect(panelKind('article', 'VideoObject')).toBe('video');
		expect(panelKind('article', 'BlogPosting')).toBe('article');
	});

	it('never second-guesses a kind someone chose', () => {
		expect(panelKind('recipe', 'Product')).toBe('recipe');
		expect(panelKind('note', 'Article')).toBe('note');
	});

	it('keeps the kind when the page has no JSON-LD', () => {
		expect(panelKind('article', undefined)).toBe('article');
		expect(panelKind('video', 'NotAType')).toBe('video');
	});
});

describe('kindFacts', () => {
	it('reads a recipe’s times and yield', () => {
		expect(
			kindFacts('recipe', {
				type: 'Recipe',
				prepTime: 'PT10M',
				totalTime: 'PT1H5M',
				yield: '12 pancakes'
			})
		).toEqual([
			{ label: 'Prep', value: '10 min' },
			{ label: 'Total', value: '1 hr 5 min' },
			{ label: 'Makes', value: '12 pancakes' }
		]);
	});

	it('shows product facts without inventing a price row', () => {
		const rows = kindFacts('product', {
			type: 'Product',
			brand: 'Acme',
			offers: { price: '99.00', currency: 'USD' },
			rating: { value: 4.6, count: 20 }
		});
		expect(rows).toContainEqual({ label: 'Brand', value: 'Acme' });
		expect(rows.some((row) => row.label === 'Rating' && row.value.includes('4.6 of 5'))).toBe(true);
	});

	it('answers no rows for an item with no details', () => {
		expect(kindFacts('recipe', undefined)).toEqual([]);
		expect(kindFacts('article', {})).toEqual([]);
	});
});

describe('hasPanel', () => {
	it('is false for an unenriched item', () => {
		expect(hasPanel('article', undefined)).toBe(false);
		expect(hasPanel('note', {})).toBe(false);
	});

	it('is false when the only thing on the page is prose the panel never shows', () => {
		expect(
			hasPanel('recipe', {
				type: 'Recipe',
				name: 'Pancakes',
				description: 'Fluffy.'
			})
		).toBe(false);
	});

	it('is true when the page carries something to show', () => {
		expect(hasPanel('article', { author: ['Jo'] })).toBe(true);
		expect(hasPanel('recipe', { ingredients: ['2 eggs'] })).toBe(true);
		expect(hasPanel('place', { address: { addressLocality: 'Lisbon' } })).toBe(true);
		expect(hasPanel('product', { offers: { price: '9.00', currency: 'USD' } })).toBe(true);
		expect(hasPanel('product', { offers: { availability: 'OutOfStock' } })).toBe(true);
	});

	it('counts a product offer only for a product panel', () => {
		// A book filed deliberately stays a book: its panel has no price row.
		expect(hasPanel('book', { type: 'Product', offers: { price: '9.00', currency: 'USD' } })).toBe(
			false
		);
	});

	it('counts the same video link the panel renders', () => {
		expect(hasPanel('video', { type: 'VideoObject', url: 'https://example.com/t' })).toBe(true);
		expect(hasPanel('video', { type: 'VideoObject', embedUrl: 'https://example.com/e' })).toBe(true);
	});
});
