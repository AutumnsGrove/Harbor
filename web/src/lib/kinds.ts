// The twelve kinds, in design.md §6 order: the labels the shell shows and the
// icons it draws beside them. One list so the item page's kind picker and the
// per-kind panel cannot drift apart.

import type { ItemKind } from '#lib/api';

export const kinds: { value: ItemKind; label: string }[] = [
	{ value: 'article', label: 'Article' },
	{ value: 'product', label: 'Product' },
	{ value: 'book', label: 'Book' },
	{ value: 'recipe', label: 'Recipe' },
	{ value: 'video', label: 'Video' },
	{ value: 'tweet', label: 'Post' },
	{ value: 'note', label: 'Note' },
	{ value: 'image', label: 'Image' },
	{ value: 'paper', label: 'Paper' },
	{ value: 'podcast', label: 'Podcast' },
	{ value: 'place', label: 'Place' },
	{ value: 'movie', label: 'Movie' }
];

// The panel headings differ where the library's word is not the reader's:
// a tweet is a post, a podcast item is an episode, a movie is a film.
export const kindHeadings: Record<ItemKind, string> = {
	article: 'Article',
	product: 'Product',
	book: 'Book',
	recipe: 'Recipe',
	video: 'Video',
	tweet: 'Post',
	note: 'Note',
	image: 'Image',
	paper: 'Paper',
	podcast: 'Episode',
	place: 'Place',
	movie: 'Film'
};

export const kindIcons: Record<ItemKind, string> = {
	article: 'file-text',
	product: 'shopping-bag',
	book: 'book',
	recipe: 'book',
	video: 'play',
	tweet: 'file-text',
	note: 'file-text',
	image: 'image',
	paper: 'file-text',
	podcast: 'play',
	place: 'map-pin',
	movie: 'play'
};
