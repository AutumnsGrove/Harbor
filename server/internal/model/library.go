package model

// Library is everything an export writes: the items, their tags, the Spaces that
// hold them, the reader's highlights and the media rows. It is one picture of the
// database, read inside one transaction, because an export whose parts disagree
// with each other is not a backup.
//
// Media holds rows, not blobs: a blob is content-addressed by sha256 and copied
// by path when the export includes media. Embeddings are deliberately absent —
// they are regenerated from text, and exporting them would suggest they are the
// copy worth keeping.
type Library struct {
	Items      []Item
	Tags       map[string][]string // item id → canonical tag names, sorted
	Spaces     []Space
	SpaceItems map[string][]string // space id → item ids, sorted
	Highlights []Highlight
	Media      []Media
}
