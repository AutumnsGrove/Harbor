// Replaying a page without fetching it: the scored set and any re-scoring
// tooling need today's model inputs from yesterday's capture, and the caps must
// come from one place or the score measures a prompt nobody ships (plans/03).
package enrich

import (
	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// PageText is an already-extracted page. It is what a saved page looks like
// once extract has run, with no fetch and no database row attached.
type PageText struct {
	URL      string
	Title    string
	SiteName string
	Excerpt  string
	Text     string
}

// ClassifyFor is the decide call's view of an extracted page, capped exactly as
// Process caps it.
func ClassifyFor(page PageText) ClassifyInput {
	return ClassifyInput{
		URL:      page.URL,
		Title:    page.Title,
		SiteName: page.SiteName,
		Excerpt:  page.Excerpt,
		Text:     truncate(page.Text, classifyTextLimit),
	}
}

// ContentFor is the tag/summary call's view of the same page. kind is the
// decision the classify call (or the owner) already made.
func ContentFor(page PageText, kind model.Kind) ContentInput {
	return ContentInput{
		URL:      page.URL,
		Title:    page.Title,
		SiteName: page.SiteName,
		Excerpt:  page.Excerpt,
		Text:     truncate(page.Text, chatTextLimit),
		Kind:     kind,
	}
}
