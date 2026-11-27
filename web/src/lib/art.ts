// The look of a thing that has no picture yet, and the shape of one that does.
// Both come from the item id, so the card in the feed and the page you open from
// it agree, and the same item looks the same on every visit.

// FNV-1a. The first version here was `h * 31 + c` with a shift, which looked
// fine and was not: over random v4 ids it filled the seven proportions very
// unevenly (chi-square about 4300, against about 3 for FNV-1a, where anything
// under 12.6 is a flat spread), so neighbouring cards kept landing on the same
// shape, the rote look this exists to break. art.test.ts pins the spread.
export function seedOf(id: string): number {
	let h = 2166136261;
	for (const ch of id) h = Math.imul(h ^ ch.charCodeAt(0), 16777619);
	return h >>> 0;
}

// Masonry only reads as masonry if the tiles differ in height. An og:image's
// real shape is unknown before it loads (and a late load must not reflow the
// column, #49), so the proportion is chosen up front, weighted by kind: video is
// wide, a book or product is tall. The image is cropped to fit.
const ratios: Record<string, string[]> = {
	video: ['16 / 9', '16 / 9', '16 / 10'],
	movie: ['2 / 3', '3 / 4', '16 / 9'],
	podcast: ['1 / 1', '1 / 1', '4 / 3'],
	book: ['3 / 4', '2 / 3', '3 / 4'],
	product: ['4 / 5', '1 / 1', '3 / 4'],
	paper: ['3 / 4', '4 / 5', '1 / 1'],
	image: ['1 / 1', '4 / 5', '3 / 4', '4 / 3', '16 / 10'],
	place: ['1 / 1', '4 / 3', '5 / 4'],
	recipe: ['4 / 5', '1 / 1', '4 / 3']
};
const fallbackRatios = ['16 / 10', '4 / 3', '1 / 1', '4 / 5', '16 / 9', '5 / 4', '3 / 4'];

export function coverRatio(kind: string, seed: number): string {
	const choices = ratios[kind] ?? fallbackRatios;
	return choices[(seed >>> 8) % choices.length] ?? '16 / 10';
}

// Where the light falls on a placeholder, as two percentages for --ax / --ay.
export function lightAt(seed: number): { x: string; y: string } {
	return { x: `${seed % 70}%`, y: `${(seed >>> 3) % 60}%` };
}
