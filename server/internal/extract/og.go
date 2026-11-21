package extract

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// OpenGraph is the metadata a page advertises about itself. It is both stored
// (items.og_json) and the fallback when readability cannot find an article:
// a JS-only page still has a title, a description and a cover.
type OpenGraph struct {
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	SiteName      string `json:"siteName,omitempty"`
	Type          string `json:"type,omitempty"`
	Image         string `json:"image,omitempty"`
	URL           string `json:"url,omitempty"`
	Author        string `json:"author,omitempty"`
	PublishedTime string `json:"publishedTime,omitempty"`
	Locale        string `json:"locale,omitempty"`
}

// readOpenGraph collects og:/twitter: meta tags. The first occurrence of a key
// wins, which is what the specifications ask for.
func readOpenGraph(sel *goquery.Document) OpenGraph {
	meta := map[string]string{}
	sel.Find("meta").Each(func(_ int, tag *goquery.Selection) {
		key := strings.ToLower(strings.TrimSpace(tag.AttrOr("property", tag.AttrOr("name", ""))))
		content, ok := tag.Attr("content")
		if key == "" || !ok {
			return
		}
		if _, exists := meta[key]; !exists {
			meta[key] = strings.TrimSpace(validUTF8(content))
		}
	})
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := meta[key]; value != "" {
				return value
			}
		}
		return ""
	}

	return OpenGraph{
		Title:         pick("og:title", "twitter:title"),
		Description:   pick("og:description", "twitter:description", "description"),
		SiteName:      pick("og:site_name", "application-name"),
		Type:          pick("og:type"),
		Image:         pick("og:image:secure_url", "og:image", "twitter:image", "twitter:image:src"),
		URL:           pick("og:url"),
		Author:        pick("article:author", "author", "twitter:creator"),
		PublishedTime: pick("article:published_time", "og:published_time", "date"),
		Locale:        pick("og:locale"),
	}
}
