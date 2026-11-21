// The typed client for the Go API. Every non-2xx response becomes an ApiError,
// because the shell has to distinguish "you are not allowed" from "the server is
// broken" instead of showing whichever one the browser guessed.

export class ApiError extends Error {
	readonly status: number;

	constructor(status: number, message: string) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
	}
}

export function isApiError(error: unknown): error is ApiError {
	return error instanceof ApiError;
}

export type VersionResponse = {
	version: string;
	deployment: string;
};

export type AvailableResponse = {
	available: boolean;
	digest?: string;
	version?: string;
};

export type UpdateStatusResponse = {
	status: string;
	version?: string;
	at?: string;
};

/** Both update endpoints answer 202 with the action they accepted. */
export type UpdateActionResponse = {
	status: string;
};

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
	const response = await fetch(path, {
		credentials: 'same-origin',
		...init,
		headers: { accept: 'application/json', ...init.headers }
	});

	if (!response.ok) {
		let message = `${response.status} ${response.statusText}`.trim();
		try {
			const body: unknown = await response.json();
			if (body && typeof body === 'object' && 'error' in body && typeof body.error === 'string') {
				message = body.error;
			}
		} catch {
			// Not JSON (a proxy error page, a truncated response). Keep the status.
		}
		throw new ApiError(response.status, message);
	}

	// 204 is a success with nothing to decode (delete, and later some admin
	// actions); `undefined as T` is the honest answer.
	if (response.status === 204) {
		return undefined as T;
	}

	return (await response.json()) as T;
}

/** A JSON body with the method, so every write goes through the same path. */
function jsonRequest(method: 'POST' | 'PATCH' | 'PUT', body: unknown): RequestInit {
	return {
		method,
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify(body)
	};
}

/**
 * The twelve kinds, in design.md §6 order. The item page shows the name; the
 * enum is what the API carries.
 */
export type ItemKind =
	| 'article'
	| 'product'
	| 'book'
	| 'recipe'
	| 'video'
	| 'tweet'
	| 'note'
	| 'image'
	| 'paper'
	| 'podcast'
	| 'place'
	| 'movie';

export type ItemSource = 'app' | 'telegram' | 'shortcut' | 'quick' | 'share' | 'import';
export type ItemState = 'queued' | 'enriching' | 'ready' | 'failed';

export type Item = {
	id: string;
	url: string | null;
	urlOriginal: string;
	title: string;
	note: string;
	summary: string;
	kind: ItemKind;
	kindSource: string;
	source: ItemSource;
	state: ItemState;
	/** Present when the item is failing enrichment. */
	attempts?: number;
	lastError?: string;
	/** Filled for one item; absent in a list so the grid stays one query. */
	tags?: string[];
	readLater?: boolean;
	createdAt: string;
	savedAt: string;
	updatedAt: string;
	deletedAt?: string;
};

/**
 * A save the Worker has accepted and the potato has not pulled yet. The card is
 * greyed out, and opening it does not claim anything (design.md §5).
 */
export type PendingJob = {
	id: string;
	title: string;
	url: string | null;
	source: ItemSource;
	kind: string;
	status: string;
	createdAt: string;
};

export type PendingResponse = {
	jobs: PendingJob[];
};

export type ItemListResponse = {
	items: Item[];
	/** Opaque; pass it back to get the next page. null when there is no more. */
	next: string | null;
};

export type SaveItemResponse = {
	item: Item;
	merged: boolean;
};

export type LookupResponse = {
	exists: boolean;
	item?: Item;
};

export type CreateItemInput = {
	url?: string;
	note?: string;
	title?: string;
	readLater?: boolean;
	source?: 'app' | 'quick' | 'share';
};

export type UpdateItemInput = {
	title?: string;
	note?: string;
	summary?: string;
	kind?: ItemKind;
};

export const api = {
	version: () => request<VersionResponse>('/api/version'),

	items: {
		/** Newest saved first. `deleted` asks for the Recently deleted view. */
		list: (options: { limit?: number; cursor?: string | null; deleted?: boolean } = {}) => {
			const query = new URLSearchParams();
			if (options.limit) query.set('limit', String(options.limit));
			if (options.cursor) query.set('cursor', options.cursor);
			if (options.deleted) query.set('deleted', 'true');
			const suffix = query.size > 0 ? `?${query}` : '';
			return request<ItemListResponse>(`/api/items${suffix}`);
		},
		get: (id: string) => request<{ item: Item }>(`/api/items/${encodeURIComponent(id)}`),
		create: (input: CreateItemInput) =>
			request<SaveItemResponse>('/api/items', jsonRequest('POST', input)),
		update: (id: string, input: UpdateItemInput) =>
			request<{ item: Item }>(`/api/items/${encodeURIComponent(id)}`, jsonRequest('PATCH', input)),
		remove: (id: string) =>
			request<void>(`/api/items/${encodeURIComponent(id)}`, { method: 'DELETE' }),
		restore: (id: string) =>
			request<{ item: Item }>(`/api/items/${encodeURIComponent(id)}/restore`, { method: 'POST' }),
		/** Puts a failed item back in the enrichment queue. */
		retry: (id: string) =>
			request<{ item: Item }>(`/api/items/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
		/** The merge question lives on the server; the UI never re-implements it. */
		lookup: (url: string) =>
			request<LookupResponse>(`/api/items/lookup?url=${encodeURIComponent(url)}`)
	},

	pending: {
		/** Read-only: the Worker's queued + claimed jobs that are not items yet. */
		list: () => request<PendingResponse>('/api/pending')
	},

	attention: {
		/** Enrichment failures, read from the item rows so a restart keeps them. */
		list: () => request<{ items: Item[] }>('/api/attention')
	},

	update: {
		/** Asks the watcher to resolve `latest`; it writes available.json later. */
		check: () => request<UpdateActionResponse>('/api/update/check', { method: 'POST' }),
		available: () => request<AvailableResponse>('/api/update/available'),
		/** Asks the watcher to apply the pinned digest. Never names an image. */
		start: () => request<UpdateActionResponse>('/api/update/start', { method: 'POST' }),
		status: () => request<UpdateStatusResponse>('/api/update/status')
	}
};
