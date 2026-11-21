// The formatters behind the per-kind item layouts. details_json is curated
// schema.org data (server/internal/extract/jsonld.go), which means ISO 8601
// durations, camel-cased availability enums, prices that arrive as strings and
// dates that are sometimes a bare year. The components render these results,
// never a raw value, and every helper answers '' for missing input so a layout
// can drop an empty row instead of printing a placeholder.

import type { ItemDetails, ItemKind } from '#lib/api';

/** One label/value pair in a facts grid. */
export type DetailsRow = { label: string; value: string };

/** facts drops rows with no value, which is what makes empty states honest. */
export function facts(rows: DetailsRow[]): DetailsRow[] {
	return rows.filter((row) => row.value.trim() !== '');
}

/**
 * humanDuration speaks ISO 8601 (`PT1H25M`, `P1DT2H`), a clock (`1:25:00`), and
 * otherwise returns the text it was given: `recipeYield` and friends are free
 * text, and inventing a unit for them would be worse than showing the source.
 */
export function humanDuration(raw: string | null | undefined): string {
	const text = (raw ?? '').trim();
	if (text === '') return '';

	const iso = /^P(?:(\d+(?:\.\d+)?)W)?(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$/i.exec(
		text
	);
	if (iso) {
		const [, weeks, days, hours, minutes, seconds] = iso;
		const total =
			Number(weeks ?? 0) * 604800 +
			Number(days ?? 0) * 86400 +
			Number(hours ?? 0) * 3600 +
			Number(minutes ?? 0) * 60 +
			Number(seconds ?? 0);
		return fromSeconds(total);
	}

	const clock = /^(\d+):(\d{2})(?::(\d{2}))?$/.exec(text);
	if (clock) {
		const [, a, b, c] = clock;
		const total =
			c === undefined ? Number(a) * 60 + Number(b) : Number(a) * 3600 + Number(b) * 60 + Number(c);
		return fromSeconds(total);
	}

	return text;
}

function fromSeconds(total: number): string {
	if (!Number.isFinite(total) || total <= 0) return '';
	const days = Math.floor(total / 86400);
	let rest = total - days * 86400;
	const hours = Math.floor(rest / 3600);
	rest -= hours * 3600;
	const minutes = Math.floor(rest / 60);
	const seconds = Math.round(rest - minutes * 60);

	const parts: string[] = [];
	if (days > 0) parts.push(`${days} day${days === 1 ? '' : 's'}`);
	if (hours > 0) parts.push(`${hours} hr`);
	if (minutes > 0) parts.push(`${minutes} min`);
	// Seconds only when they are the whole story; "1 hr 3 min 12 sec" is noise.
	if (parts.length === 0 && seconds > 0) parts.push(`${seconds} sec`);
	return parts.join(' ');
}

/** dateLabel accepts a year, an ISO date, or unparseable text without lying. */
export function dateLabel(raw: string | null | undefined): string {
	const text = (raw ?? '').trim();
	if (text === '') return '';
	if (/^\d{4}$/.test(text)) return text;
	// A date with no time is a calendar day, not an instant: formatting it in
	// the viewer's timezone shows the day before for anyone west of UTC.
	const dateOnly = /^(\d{4})-(\d{2})-(\d{2})$/.exec(text);
	if (dateOnly) {
		const [, year, month, day] = dateOnly;
		const calendar = new Date(Date.UTC(Number(year), Number(month) - 1, Number(day)));
		if (Number.isNaN(calendar.getTime())) return text;
		return calendar.toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
			timeZone: 'UTC'
		});
	}
	const parsed = new Date(text);
	if (Number.isNaN(parsed.getTime())) return text;
	return parsed.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

/**
 * priceLabel formats the currency the page declared. It falls back to
 * "12.50 EUR" when the code is not a currency Intl knows, rather than dropping
 * a price someone may want to read.
 */
export function priceLabel(offers: ItemDetails['offers']): string {
	const price = (offers?.price ?? '').trim();
	if (price === '') return '';
	const currency = (offers?.currency ?? '').trim().toUpperCase();
	if (/^[A-Z]{3}$/.test(currency)) {
		const amount = Number(price.replace(/[^0-9.]/g, ''));
		if (Number.isFinite(amount) && amount > 0) {
			try {
				return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount);
			} catch {
				// Not a currency Intl knows; the plain form below is still true.
			}
		}
	}
	return currency === '' ? price : `${price} ${currency}`;
}

const availability: Record<string, string> = {
	instock: 'In stock',
	outofstock: 'Out of stock',
	preorder: 'Pre-order',
	soldout: 'Sold out',
	limitedavailability: 'Limited availability',
	instoreonly: 'In store only',
	onlineonly: 'Online only',
	backorder: 'On back order',
	discontinued: 'Discontinued',
	presale: 'Pre-sale'
};

/** availabilityLabel turns schema.org's `InStock` enum into words. */
export function availabilityLabel(raw: string | null | undefined): string {
	const text = (raw ?? '').trim();
	if (text === '') return '';
	const known = availability[text.toLowerCase()];
	if (known !== undefined) return known;
	// An enum the map has not met yet: "MadeToOrder" -> "Made to order".
	const spaced = text.replace(/([a-z])([A-Z])/g, '$1 $2').toLowerCase();
	return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
/** ratingLabel is "4.6 of 5", plus a count only when the page gave one. */
export function ratingLabel(rating: ItemDetails['rating']): string {
	const value = Number(rating?.value ?? '');
	if (!Number.isFinite(value) || value <= 0) return '';
	const best = Number(rating?.best ?? 0);
	const outOf = Number.isFinite(best) && best > 0 ? best : 5;
	const shown = Number.isInteger(value) ? String(value) : value.toFixed(1);
	const count = Number(rating?.count ?? 0);
	if (Number.isFinite(count) && count > 0) {
		return `${shown} of ${outOf} · ${count.toLocaleString()} ratings`;
	}
	return `${shown} of ${outOf}`;
}

/** addressLines is one line per printed line, in postal order. */
export function addressLines(address: ItemDetails['address']): string[] {
	if (!address) return [];
	const street = (address.streetAddress ?? '').trim();
	const region = [address.addressRegion, address.postalCode]
		.map((part) => (part ?? '').trim())
		.filter((part) => part !== '')
		.join(' ');
	const locality = [address.addressLocality, region]
		.map((part) => (part ?? '').trim())
		.filter((part) => part !== '')
		.join(', ');
	const country = (address.addressCountry ?? '').trim();
	return [street, locality, country].filter((line) => line !== '');
}

// --- which panel a page gets ------------------------------------------------

// typeToKind is the schema.org type back to the layout that shows it. The
// extractor folded subtypes already (restaurant → Place), so the names are the
// curated ones from extract/jsonld.go.
const typeToKind: Record<string, ItemKind> = {
	Recipe: 'recipe',
	Product: 'product',
	Book: 'book',
	Movie: 'movie',
	Place: 'place',
	VideoObject: 'video',
	ScholarlyArticle: 'paper',
	Article: 'article',
	NewsArticle: 'article',
	BlogPosting: 'article',
	TechArticle: 'article',
	Report: 'article'
};

/**
 * panelKind picks the layout that actually has data. The library's kind wins,
 * except when it is the `article` fallback and the page's JSON-LD names a
 * specific shape — then the recipe a model filed as an article still renders
 * its ingredients. A deliberate kind is never second-guessed.
 */
export function panelKind(kind: ItemKind, type: string | null | undefined): ItemKind {
	if (kind !== 'article') return kind;
	return typeToKind[(type ?? '').trim()] ?? kind;
}

/**
 * kindFacts is the facts grid for one panel: the rows a page really carries,
 * in reading order. Rows with no value are dropped (facts), so an item with no
 * JSON-LD renders no panel at all rather than a grid of dashes.
 */
export function kindFacts(kind: ItemKind, details: ItemDetails | undefined): DetailsRow[] {
	const d = details ?? {};
	switch (panelKind(kind, d.type)) {
		case 'recipe':
			return facts([
				{ label: 'Prep', value: humanDuration(d.prepTime) },
				{ label: 'Cook', value: humanDuration(d.cookTime) },
				{ label: 'Total', value: humanDuration(d.totalTime) },
				{ label: 'Makes', value: (d.yield ?? '').trim() },
				{ label: 'Cuisine', value: (d.cuisine ?? '').trim() },
				{ label: 'Course', value: (d.category ?? '').trim() }
			]);
		case 'product':
			return facts([
				{ label: 'Brand', value: (d.brand ?? '').trim() },
				{ label: 'Rating', value: ratingLabel(d.rating) },
				{ label: 'Author', value: (d.author ?? []).join(', ') }
			]);
		case 'place':
			return facts([{ label: 'Phone', value: (d.telephone ?? '').trim() }]);
		case 'video':
			return facts([
				{ label: 'Length', value: humanDuration(d.duration) },
				{ label: 'Published', value: dateLabel(d.uploadDate || d.published) },
				{ label: 'Channel', value: (d.author ?? []).join(', ') }
			]);
		case 'book':
			return facts([
				{ label: 'Author', value: (d.author ?? []).join(', ') },
				{ label: 'Published', value: dateLabel(d.published) },
				{ label: 'Publisher', value: (d.publisher ?? '').trim() },
				{ label: 'Pages', value: d.pages === undefined ? '' : String(d.pages) },
				{ label: 'ISBN', value: (d.isbn ?? '').trim() },
				{ label: 'Genre', value: (d.genre ?? []).join(', ') }
			]);
		case 'movie':
			return facts([
				{ label: 'Director', value: (d.director ?? []).join(', ') },
				{ label: 'Runtime', value: humanDuration(d.duration) },
				{ label: 'Released', value: dateLabel(d.published) },
				{ label: 'Rating', value: ratingLabel(d.rating) },
				{ label: 'Genre', value: (d.genre ?? []).join(', ') }
			]);
		case 'paper':
			return facts([
				{ label: 'Author', value: (d.author ?? []).join(', ') },
				{ label: 'Published', value: dateLabel(d.published) },
				{ label: 'Publisher', value: (d.publisher ?? '').trim() }
			]);
		default:
			return facts([
				{ label: 'Author', value: (d.author ?? []).join(', ') },
				{ label: 'Published', value: dateLabel(d.published) },
				{ label: 'Updated', value: dateLabel(d.updated) },
				{ label: 'Section', value: (d.section ?? '').trim() }
			]);
	}
}

/**
 * hasPanel says whether the layout has anything to show. The generic article
 * rows count only when one of them carries a value, so an unenriched item does
 * not grow an empty "Article" section.
 */
export function hasPanel(kind: ItemKind, details: ItemDetails | undefined): boolean {
	const d = details ?? {};
	const shape = panelKind(kind, d.type);
	if (kindFacts(kind, d).length > 0) return true;
	if ((d.description ?? '').trim() !== '' || (d.abstract ?? '').trim() !== '') return true;
	if ((d.ingredients ?? []).length > 0 || (d.instructions ?? []).length > 0) return true;
	if ((d.openingHours ?? []).length > 0) return true;
	if (addressLines(d.address).length > 0) return true;
	if (priceLabel(d.offers) !== '') return true;
	return shape === 'video' && (d.embedUrl ?? '') !== '';
}
