package extract

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// schema.org types this package understands, in the order a page with several
// of them should be trusted. A WebSite/Organization block never wins over the
// thing the page is about.
var detailTypePriority = []string{
	"recipe", "product", "book", "movie", "place", "videoobject",
	"scholarlyarticle", "article", "newsarticle", "blogposting", "techarticle", "report",
}

var detailTypeNames = map[string]string{
	"recipe": "Recipe", "product": "Product", "book": "Book", "movie": "Movie",
	"place": "Place", "videoobject": "VideoObject", "scholarlyarticle": "ScholarlyArticle",
	"article": "Article", "newsarticle": "NewsArticle", "blogposting": "BlogPosting",
	"techarticle": "TechArticle", "report": "Report",
}

// detailTypeAliases maps schema.org subtypes to the layout that can show them.
// A bakery is a place; a restaurant is a place with hours. Keeping one layout
// for the family is better than a "Bakery" branch that renders nothing.
var detailTypeAliases = map[string]string{
	"restaurant": "place", "cafeorcoffeeshop": "place", "bakery": "place",
	"barorpub": "place", "foodestablishment": "place", "localbusiness": "place",
	"store": "place", "museum": "place", "touristattraction": "place",
	"newsarticle": "article", "blogposting": "article", "techarticle": "article",
	"report": "article", "webpage": "article",
}

// canonicalType folds a subtype into the layout family, if it has one.
func canonicalType(name string) string {
	if mapped, ok := detailTypeAliases[name]; ok {
		return mapped
	}
	return name
}

// readJSONLD collects every schema.org object on the page and curates the most
// useful one into details_json. Malformed JSON is ignored: a page with a broken
// script still extracts, it just has no details (plans/03 test 19).
func readJSONLD(sel *goquery.Document) map[string]any {
	var objects []map[string]any
	sel.Find("script").Each(func(_ int, tag *goquery.Selection) {
		kind := strings.TrimSpace(tag.AttrOr("type", ""))
		if !strings.EqualFold(kind, "application/ld+json") {
			return
		}
		raw := strings.TrimSpace(tag.Text())
		// Some CMSs wrap the JSON in an HTML comment to hide it from old
		// browsers; that is the only cleanup worth doing.
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "<!--"), "-->")
		if raw == "" {
			return
		}
		var document any
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			return
		}
		collectObjects(document, &objects)
	})

	chosen := chooseDetailObject(objects)
	if chosen == nil {
		return map[string]any{}
	}
	return sanitiseMap(detailsFor(chosen))
}

// sanitiseMap makes every string in the details tree valid UTF-8. JSON-LD is
// attacker-controlled text and the column is read by a browser.
func sanitiseMap(values map[string]any) map[string]any {
	for key, value := range values {
		values[key] = sanitiseValue(value)
	}
	return values
}

func sanitiseValue(value any) any {
	switch typed := value.(type) {
	case string:
		return validUTF8(typed)
	case []string:
		for i, item := range typed {
			typed[i] = validUTF8(item)
		}
		return typed
	case []any:
		for i, item := range typed {
			typed[i] = sanitiseValue(item)
		}
		return typed
	case map[string]any:
		return sanitiseMap(typed)
	default:
		return value
	}
}

// collectObjects flattens @graph and arrays into the candidate list.
func collectObjects(value any, out *[]map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		if _, hasType := typed["@type"]; hasType {
			*out = append(*out, typed)
		}
		if graph, ok := typed["@graph"]; ok {
			collectObjects(graph, out)
		}
	case []any:
		for _, item := range typed {
			collectObjects(item, out)
		}
	}
}

func chooseDetailObject(objects []map[string]any) map[string]any {
	for _, wanted := range detailTypePriority {
		for _, object := range objects {
			for _, name := range objectTypes(object) {
				if canonicalType(name) == wanted {
					return object
				}
			}
		}
	}
	return nil
}

// objectTypes lowercases @type, accepting a string, an array, and a full
// schema.org URL ("https://schema.org/Recipe").
func objectTypes(object map[string]any) []string {
	var raw []string
	switch typed := object["@type"].(type) {
	case string:
		raw = append(raw, typed)
	case []any:
		for _, value := range typed {
			if text, ok := value.(string); ok {
				raw = append(raw, text)
			}
		}
	}
	names := make([]string, 0, len(raw))
	for _, value := range raw {
		if slash := strings.LastIndex(value, "/"); slash >= 0 {
			value = value[slash+1:]
		}
		names = append(names, strings.ToLower(strings.TrimSpace(value)))
	}
	return names
}

// detailsFor copies the fields the per-kind layouts can show, normalising the
// shapes JSON-LD allows (a string or a list, a Person or a name) so the UI has
// one form to render.
func detailsFor(object map[string]any) map[string]any {
	details := map[string]any{}
	for _, name := range objectTypes(object) {
		if canonical, ok := detailTypeNames[canonicalType(name)]; ok {
			details["type"] = canonical
			break
		}
	}
	setString(details, "name", firstNonEmpty(text(object["name"]), text(object["headline"])))
	setString(details, "description", text(object["description"]))
	setString(details, "url", text(object["url"]))
	setString(details, "image", firstImage(object["image"]))
	setStrings(details, "author", nameList(object["author"]))
	setString(details, "published", text(object["datePublished"]))
	setString(details, "updated", text(object["dateModified"]))

	switch details["type"] {
	case "Recipe":
		setStrings(details, "ingredients", textList(object["recipeIngredient"]))
		setStrings(details, "instructions", instructions(object["recipeInstructions"]))
		setString(details, "prepTime", text(object["prepTime"]))
		setString(details, "cookTime", text(object["cookTime"]))
		setString(details, "totalTime", text(object["totalTime"]))
		setString(details, "yield", text(object["recipeYield"]))
		setString(details, "cuisine", text(object["recipeCuisine"]))
		setString(details, "category", text(object["recipeCategory"]))
	case "Product":
		setString(details, "brand", text(object["brand"]))
		setMap(details, "offers", offer(object["offers"]))
		setMap(details, "rating", rating(object["aggregateRating"]))
	case "Book":
		setString(details, "isbn", firstText(object["isbn"]))
		setNumber(details, "pages", number(object["numberOfPages"]))
		setString(details, "publisher", text(object["publisher"]))
		setStrings(details, "genre", textList(object["genre"]))
	case "Movie":
		setStrings(details, "director", nameList(object["director"]))
		setString(details, "duration", text(object["duration"]))
		setStrings(details, "genre", textList(object["genre"]))
		setMap(details, "rating", rating(object["aggregateRating"]))
	case "Place":
		setMap(details, "address", address(object["address"]))
		setString(details, "telephone", text(object["telephone"]))
		setStrings(details, "openingHours", textList(object["openingHours"]))
	case "VideoObject":
		setString(details, "duration", text(object["duration"]))
		setString(details, "embedUrl", text(object["embedUrl"]))
		setString(details, "thumbnailUrl", firstImage(object["thumbnailUrl"]))
		setString(details, "uploadDate", text(object["uploadDate"]))
	case "ScholarlyArticle":
		setString(details, "abstract", text(object["abstract"]))
	case "Article", "NewsArticle", "BlogPosting", "TechArticle", "Report":
		setString(details, "section", text(object["articleSection"]))
	}
	return details
}

// --- value normalisers -----------------------------------------------------

// text flattens the shapes a JSON-LD scalar takes: a string, a number, or an
// object with a name/value.
func text(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case map[string]any:
		for _, key := range []string{"name", "text", "@value", "url"} {
			if nested := text(typed[key]); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func textList(value any) []string {
	switch typed := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	case map[string]any:
		if nested := text(typed); nested != "" {
			return []string{nested}
		}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, textList(item)...)
		}
		return out
	}
	return nil
}

// nameList is textList for things that may be a Person/Organization object.
func nameList(value any) []string {
	switch typed := value.(type) {
	case map[string]any:
		if name := text(typed["name"]); name != "" {
			return []string{name}
		}
		// An array is sometimes wrapped in an object; fall through.
		return textList(typed["author"])
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, nameList(item)...)
		}
		return out
	default:
		return textList(value)
	}
}

func firstText(value any) string {
	if list := textList(value); len(list) > 0 {
		return list[0]
	}
	return ""
}

// firstImage accepts a URL string, an ImageObject, or a list of either.
func firstImage(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"url", "contentUrl", "thumbnailUrl"} {
			if url := text(typed[key]); url != "" {
				return url
			}
		}
	case []any:
		for _, item := range typed {
			if url := firstImage(item); url != "" {
				return url
			}
		}
	}
	return ""
}

// instructions accepts the three shapes recipeInstructions uses: one string,
// a list of strings, or HowToStep/HowToSection objects.
func instructions(value any) []string {
	switch typed := value.(type) {
	case string:
		var out []string
		for _, line := range strings.Split(typed, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case map[string]any:
		if section := typed["itemListElement"]; section != nil {
			return instructions(section)
		}
		if step := text(typed["text"]); step != "" {
			return []string{step}
		}
		if name := text(typed["name"]); name != "" {
			return []string{name}
		}
	case []any:
		var out []string
		for _, item := range typed {
			out = append(out, instructions(item)...)
		}
		return out
	}
	return nil
}

func offer(value any) map[string]any {
	if list, ok := value.([]any); ok && len(list) > 0 {
		value = list[0]
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	if price := text(object["price"]); price != "" {
		out["price"] = price
	}
	setString(out, "currency", text(object["priceCurrency"]))
	if availability := text(object["availability"]); availability != "" {
		if slash := strings.LastIndex(availability, "/"); slash >= 0 {
			availability = availability[slash+1:]
		}
		out["availability"] = availability
	}
	setString(out, "url", text(object["url"]))
	if len(out) == 0 {
		return nil
	}
	return out
}

func rating(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	setNumber(out, "value", number(object["ratingValue"]))
	setNumber(out, "best", number(object["bestRating"]))
	setNumber(out, "count", number(firstValue(object["ratingCount"], object["reviewCount"])))
	if len(out) == 0 {
		return nil
	}
	return out
}

func address(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		if list := textList(value); len(list) > 0 {
			return map[string]any{"streetAddress": list[0]}
		}
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"streetAddress", "addressLocality", "addressRegion", "postalCode", "addressCountry"} {
		setString(out, key, text(object[key]))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// number keeps an integral value integral in JSON; a float is left alone.
func number(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) {
			return int64(typed)
		}
		return typed
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			if parsed == math.Trunc(parsed) {
				return int64(parsed)
			}
			return parsed
		}
	}
	return nil
}

// --- map setters -----------------------------------------------------------

func setString(target map[string]any, key, value string) {
	if value != "" {
		target[key] = value
	}
}

func setStrings(target map[string]any, key string, values []string) {
	if len(values) > 0 {
		target[key] = values
	}
}

func setNumber(target map[string]any, key string, value any) {
	if value != nil {
		target[key] = value
	}
}

func setMap(target map[string]any, key string, value map[string]any) {
	if len(value) > 0 {
		target[key] = value
	}
}
