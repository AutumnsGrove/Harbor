package extract

import (
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
)

// toMarkdown converts readability's content node. The converter takes the
// parsed node directly, so the HTML is not parsed a second time; the reader in
// phase 8 renders this Markdown (design.md §6 step 2).
func toMarkdown(node *html.Node) (string, error) {
	markdown, err := htmltomarkdown.ConvertNode(node)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(markdown)), nil
}
