package planrefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/net/html"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const (
	// maxPageBytes caps what a vendor page is read as: a page over it is refused
	// rather than read from its first megabyte.
	maxPageBytes = 1 << 20
	// maxModelsBytes caps what a models endpoint's reply is read as.
	maxModelsBytes = 1 << 20
)

// Vendor reads a plan's own sources: each vendor page its definition names, and
// the vendor's OpenAI-compatible models endpoint.
type Vendor struct {
	// HTTP is the client requests go through; nil means NewClient().
	HTTP *http.Client
	// Base resolves a provider's OpenAI-compatible API base; nil means the
	// embedded provider configuration's own.
	Base func(provider string) string
}

// Page reads the region of one vendor page the locator names and returns the
// page's own words. The selector is the region to read — an #id, a .class or a
// tag name — and an empty selector reads the whole page.
func (v Vendor) Page(ctx context.Context, loc providers.Locator) (string, error) {
	page, err := readBody(ctx, v.HTTP, loc.URL, maxPageBytes)
	if err != nil {
		return "", err
	}
	return selectText(string(page), loc.Selector)
}

// Models reads provider's own model list from its models endpoint.
func (v Vendor) Models(ctx context.Context, provider string) ([]string, error) {
	base := providers.BaseURL(provider)
	if v.Base != nil {
		base = v.Base(provider)
	}
	if base == "" {
		return nil, fmt.Errorf("provider %s has no API base to read a model list from", provider)
	}
	body, err := readBody(ctx, v.HTTP, strings.TrimSuffix(base, "/")+"/models", maxModelsBytes)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("decoding the model list: %w", err)
	}
	models := make([]string, 0, len(reply.Data))
	for _, m := range reply.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			models = append(models, id)
		}
	}
	return capList(models, maxModels), nil
}

// selectText returns the words of the region selector names, with runs of
// whitespace collapsed to one space. A selector the page does not match is
// refused, so a page whose layout moved is recorded as unreadable rather than as
// a region that emptied.
func selectText(page, selector string) (string, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return "", fmt.Errorf("parsing the page: %w", err)
	}
	region := doc
	if selector != "" {
		match, err := matchRegion(doc, selector)
		if err != nil {
			return "", err
		}
		region = match
	}
	var b strings.Builder
	words(&b, region)
	return strings.Join(strings.Fields(b.String()), " "), nil
}

// matchRegion returns the first element the selector names.
func matchRegion(doc *html.Node, selector string) (*html.Node, error) {
	match, err := matcher(selector)
	if err != nil {
		return nil, err
	}
	if n := find(doc, match); n != nil {
		return n, nil
	}
	return nil, fmt.Errorf("the page has no %s", selector)
}

// matcher returns the test one selector matches an element by. It supports the
// three shapes a plan's definition is written in — #id, .class and a tag name —
// and refuses any other rather than matching it as a tag name it is not.
func matcher(selector string) (func(*html.Node) bool, error) {
	switch {
	case strings.HasPrefix(selector, "#") && isName(selector[1:]):
		id := selector[1:]
		return func(n *html.Node) bool { return attribute(n, "id") == id }, nil
	case strings.HasPrefix(selector, ".") && isName(selector[1:]):
		class := selector[1:]
		return func(n *html.Node) bool { return hasClass(n, class) }, nil
	case isName(selector):
		return func(n *html.Node) bool { return n.Data == selector }, nil
	}
	return nil, fmt.Errorf("selector %q is not an #id, a .class or a tag name", selector)
}

// isName reports whether s is a tag, an id or a class name and nothing else, so a
// richer selector is refused rather than read as the first of its parts.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// find returns the first element in document order that match accepts, or nil
// when none does.
func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := find(c, match); found != nil {
			return found
		}
	}
	return nil
}

// attribute is the value of an element's own attribute, empty when it has none.
func attribute(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// hasClass reports whether an element carries a class, which is one name among
// the attribute's space-separated ones.
func hasClass(n *html.Node, class string) bool {
	return slices.Contains(strings.Fields(attribute(n, "class")), class)
}

// words appends the words of node and its descendants to b, leaving out a
// script's and a style's own contents, which are not the page's words.
func words(b *strings.Builder, n *html.Node) {
	switch {
	case n.Type == html.TextNode:
		b.WriteString(n.Data)
		b.WriteByte(' ')
		return
	case n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style"):
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		words(b, c)
	}
}
