/**
 * Split a list into display columns so the grid reads left to right.
 *
 * CSS multi-column layout fills one column top to bottom before starting the
 * next, which presents a newest-first list *down* the page instead of across it
 * (#51). Round-robin puts item i in column i % N, so the top row is saves 1..N in
 * save order, and appending to the list never moves a card that is already on
 * screen: with a fixed N, a card's column depends only on its index.
 *
 * The trade-off is the one Raindrop has: the DOM is column-major, so tab and
 * screen-reader order follow the columns while the eye follows the rows.
 */
export function splitColumns<T>(items: readonly T[], columns: number): T[][] {
	const count = Number.isFinite(columns) && columns >= 1 ? Math.floor(columns) : 1;
	const result: T[][] = Array.from({ length: count }, () => []);

	items.forEach((item, index) => {
		const column = result[index % count];
		if (column) column.push(item);
	});

	return result;
}
