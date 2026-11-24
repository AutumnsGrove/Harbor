import { describe, expect, it } from 'vitest';

import { splitColumns } from './feed';

// #51: the grid has to read left to right. What the browser actually paints is a
// row-major order, and jsdom cannot measure that (every rect is zero), so the
// assertion lives on the rule the layout is built from: where each item lands.

const eight = [1, 2, 3, 4, 5, 6, 7, 8];

describe('splitColumns', () => {
	it('fills the top row with the newest saves in order', () => {
		const columns = splitColumns(eight, 4);

		expect(columns.map((column) => column[0])).toEqual([1, 2, 3, 4]);
		expect(columns).toEqual([
			[1, 5],
			[2, 6],
			[3, 7],
			[4, 8]
		]);
	});

	it('agrees with the flat list when the columns are read as a grid', () => {
		// Read row by row: the first item of every column, then the second.
		const columns = splitColumns(eight, 4);
		const rowMajor = Array.from({ length: 2 }, (_, row) =>
			columns.map((column) => column[row])
		).flat();

		expect(rowMajor).toEqual(eight);
	});

	it('does not move a card that is already on screen when more are appended', () => {
		const before = splitColumns(eight, 4);
		const after = splitColumns([...eight, 9, 10], 4);

		eight.forEach((value, index) => {
			expect(after[index % 4]).toContain(value);
		});
		// Everything that was there stays in the same column, in the same place.
		before.forEach((column, index) => {
			expect(after[index]?.slice(0, column.length)).toEqual(column);
		});
	});

	it('handles fewer items than columns, one column, and a stale count', () => {
		expect(splitColumns([1, 2], 4)).toEqual([[1], [2], [], []]);
		expect(splitColumns([1, 2, 3], 1)).toEqual([[1, 2, 3]]);
		expect(splitColumns([1], 0)).toEqual([[1]]);
		expect(splitColumns([1], -3)).toEqual([[1]]);
		expect(splitColumns([], 4)).toEqual([[], [], [], []]);
	});
});
