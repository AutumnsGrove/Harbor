// The Netscape bookmarks writer: the file every browser and every bookmark tool
// can read, and the one phase 5's importer reads (plans/12a).
package export

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// netscapeFile writes bookmarks.html. It carries live, URL-bearing items only:
// a note has no bookmark, and a soft-deleted item is not something to hand a
// browser. library.json is the complete copy; this file is the readable one.
func netscapeFile(library model.Library, entries []itemEntry) []byte {
	manual := map[string]bool{}
	for _, space := range library.Spaces {
		if string(space.Kind) == "manual" {
			manual[space.ID] = true
		}
	}

	// Folder is the item's first manual Space, or the top level when it has none.
	folders := map[string][]itemEntry{}
	var loose []itemEntry
	for _, entry := range entries {
		if entry.URL == nil || entry.DeletedAt != nil {
			continue
		}
		folder := ""
		for _, space := range entry.Spaces {
			if manual[space.ID] {
				folder = space.Name
				break
			}
		}
		if folder == "" {
			loose = append(loose, entry)
			continue
		}
		folders[folder] = append(folders[folder], entry)
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE NETSCAPE-Bookmark-file-1>\n")
	b.WriteString("<!-- This is an automatically generated file.\n")
	b.WriteString("     It will be read and overwritten. DO NOT EDIT! -->\n")
	b.WriteString(`<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">` + "\n")
	b.WriteString("<TITLE>Bookmarks</TITLE>\n")
	b.WriteString("<H1>Bookmarks</H1>\n")
	b.WriteString("<DL><p>\n")

	for _, entry := range loose {
		writeBookmark(&b, entry, "    ")
	}

	names := make([]string, 0, len(folders))
	for name := range folders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString("    <DT><H3>" + html.EscapeString(name) + "</H3>\n")
		b.WriteString("    <DL><p>\n")
		for _, entry := range folders[name] {
			writeBookmark(&b, entry, "        ")
		}
		b.WriteString("    </DL><p>\n")
	}

	b.WriteString("</DL><p>\n")
	return []byte(b.String())
}

func writeBookmark(b *strings.Builder, entry itemEntry, indent string) {
	title := strings.TrimSpace(entry.Title)
	if title == "" && entry.URL != nil {
		title = *entry.URL
	}
	fmt.Fprintf(b, `%s<DT><A HREF="%s" ADD_DATE="%d"`,
		indent, html.EscapeString(*entry.URL), addDate(entry.SavedAt))
	if len(entry.Tags) > 0 {
		b.WriteString(` TAGS="` + html.EscapeString(strings.Join(entry.Tags, ",")) + `"`)
	}
	b.WriteString(">" + html.EscapeString(title) + "</A>\n")
}

// addDate is what a browser reads: unix seconds. An unparseable stamp becomes
// zero rather than dropping the bookmark, because the URL is the part that
// matters.
func addDate(stamp string) int64 {
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}
