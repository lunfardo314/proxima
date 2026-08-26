// Package logo holds the Proxima logo and puts it into the HTML pages the node
// serves. Three shapes — the bare Centaurus mark, the horizontal lockup and the
// stacked lockup — each in a light-background and a dark-background variant.
//
// The drawing is stroked in `currentColor` throughout, so a page that inlines
// it recolors the mark from CSS and either variant would do. The variants
// differ in what they fall back to when there is no CSS to inherit from: a
// favicon, an <img src=…>, the file opened on its own.
package logo

import (
	"bytes"
	_ "embed"
	"encoding/base64"
)

var (
	//go:embed proxima-centaur-min-onlight.svg
	MarkOnLight []byte
	//go:embed proxima-centaur-min-ondark.svg
	MarkOnDark []byte
	//go:embed proxima-lockup-onlight.svg
	LockupOnLight []byte
	//go:embed proxima-lockup-ondark.svg
	LockupOnDark []byte
	//go:embed proxima-lockup-stacked-onlight.svg
	StackedOnLight []byte
	//go:embed proxima-lockup-stacked-ondark.svg
	StackedOnDark []byte
)

// DataURI renders a variant as a data: URI — the form <link rel="icon"> needs,
// since the pages are single self-contained files with no assets to fetch.
func DataURI(svg []byte) string {
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(svg)
}

// Page substitutes the logo into a page template: the <!--LOGO--> comment with
// the inline SVG, %FAVICON% with the tab icon as a data URI. Pages carry the
// placeholders they want; a missing one costs nothing.
func Page(html, inline, favicon []byte) []byte {
	html = bytes.Replace(html, []byte("<!--LOGO-->"), inline, 1)
	return bytes.Replace(html, []byte("%FAVICON%"), []byte(DataURI(favicon)), 1)
}
