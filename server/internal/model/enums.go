package model

// Kind is what an item is. There are twelve and never "unknown": article is
// the catch-all for written pages and unclassifiable links, note is what a
// URL-less item is written as at insert time (design.md §6 step 4, §7).
type Kind string

const (
	KindArticle Kind = "article"
	KindProduct Kind = "product"
	KindBook    Kind = "book"
	KindRecipe  Kind = "recipe"
	KindVideo   Kind = "video"
	KindTweet   Kind = "tweet"
	KindNote    Kind = "note"
	KindImage   Kind = "image"
	KindPaper   Kind = "paper"
	KindPodcast Kind = "podcast"
	KindPlace   Kind = "place"
	KindMovie   Kind = "movie"
)

// Kinds lists the twelve, in the order design.md names them.
func Kinds() []Kind {
	return []Kind{
		KindArticle, KindProduct, KindBook, KindRecipe, KindVideo, KindTweet,
		KindNote, KindImage, KindPaper, KindPodcast, KindPlace, KindMovie,
	}
}

// Valid reports whether k is one of the twelve.
func (k Kind) Valid() bool { return contains(Kinds(), k) }

// State is where an item sits in the potato's enrichment queue (items.state).
// It is not the Worker's job status: that queue is delivery, this one is work.
type State string

const (
	StateQueued    State = "queued"
	StateEnriching State = "enriching"
	StateReady     State = "ready"
	StateFailed    State = "failed"
)

func States() []State {
	return []State{StateQueued, StateEnriching, StateReady, StateFailed}
}

func (s State) Valid() bool { return contains(States(), s) }

// Source is the door an item came through (items.source). telegram and
// shortcut are the Worker's doors and are the values contracts/job.schema.json
// allows; the rest never pass through the Worker.
type Source string

const (
	SourceApp      Source = "app"
	SourceTelegram Source = "telegram"
	SourceShortcut Source = "shortcut"
	SourceQuick    Source = "quick"
	SourceShare    Source = "share"
	SourceImport   Source = "import"
)

func Sources() []Source {
	return []Source{SourceApp, SourceTelegram, SourceShortcut, SourceQuick, SourceShare, SourceImport}
}

func (s Source) Valid() bool { return contains(Sources(), s) }

// KindSource records who decided an item's kind (items.kind_source). A manual
// kind is never overwritten by enrichment.
type KindSource string

const (
	KindSourceDefault KindSource = "default"
	KindSourceAI      KindSource = "ai"
	KindSourceManual  KindSource = "manual"
)

func KindSources() []KindSource {
	return []KindSource{KindSourceDefault, KindSourceAI, KindSourceManual}
}

func (s KindSource) Valid() bool { return contains(KindSources(), s) }

// TagSource records who attached a tag (item_tags.source).
type TagSource string

const (
	TagSourceAI     TagSource = "ai"
	TagSourceManual TagSource = "manual"
)

func TagSources() []TagSource { return []TagSource{TagSourceAI, TagSourceManual} }

func (s TagSource) Valid() bool { return contains(TagSources(), s) }

// MediaKind is the class of an attachment (media.kind). Thumbnails are derived
// files, not rows.
type MediaKind string

const (
	MediaImage MediaKind = "image"
	MediaPDF   MediaKind = "pdf"
	MediaFile  MediaKind = "file"
)

func MediaKinds() []MediaKind { return []MediaKind{MediaImage, MediaPDF, MediaFile} }

func (k MediaKind) Valid() bool { return contains(MediaKinds(), k) }

// SpaceKind says how a Space's members are chosen (spaces.kind).
type SpaceKind string

const (
	SpaceAuto   SpaceKind = "auto"
	SpaceSaved  SpaceKind = "saved"
	SpaceManual SpaceKind = "manual"
)

func SpaceKinds() []SpaceKind { return []SpaceKind{SpaceAuto, SpaceSaved, SpaceManual} }

func (k SpaceKind) Valid() bool { return contains(SpaceKinds(), k) }

// HighlightColor is the reader highlight palette (highlights.color).
type HighlightColor string

const (
	ColorAmber   HighlightColor = "amber"
	ColorMagenta HighlightColor = "magenta"
	ColorTeal    HighlightColor = "teal"
	ColorBlue    HighlightColor = "blue"
)

func HighlightColors() []HighlightColor {
	return []HighlightColor{ColorAmber, ColorMagenta, ColorTeal, ColorBlue}
}

func (c HighlightColor) Valid() bool { return contains(HighlightColors(), c) }

func contains[T comparable](all []T, v T) bool {
	for _, candidate := range all {
		if candidate == v {
			return true
		}
	}
	return false
}
