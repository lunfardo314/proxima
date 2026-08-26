# The Proxima logo

Six SVG files and forty lines of Go. Read this before editing the artwork or
adding the logo to a new page.

## The mark

Centaurus, line-drawn: the constellation's main stars as filled dots, the
head-and-body figure as thin joined strokes, and **Proxima Centauri** as the red
star with the dashed ring around it. Proxima is the nearest star to the Sun and
the faintest thing in that picture — the ring is there because otherwise nobody
finds it.

Every stroke and dot is `currentColor`. Only the Proxima star keeps a literal
color, since it is the one element that means something by being red.

## The wordmark

Monoline geometric caps, drawn as stroked paths on a 26-unit cap height with
round caps and joins — the same drawing vocabulary as the constellation lines,
so mark and name read as one drawing rather than a picture with a caption.

**It is outlined, not typeset.** No `<text>`, no font-family, no webfont. These
files are inlined into self-contained pages and encoded into `data:` favicons,
where there is no font to load and no guarantee about what is installed. The
letterforms are geometric (circular `O`, semicircular `P`/`R` bowls, a
full-depth `M` vertex) which is what makes them drawable in a dozen paths.

Letter spacing is **optical, not metric**: round-to-round and round-to-diagonal
pairs are set tighter than stem-to-stem ones. Changing one letter's width means
re-walking the advances of everything after it.

## The six files

|  | Light backgrounds | Dark backgrounds |
|--|-------------------|------------------|
| Mark alone | `proxima-centaur-min-onlight.svg` | `proxima-centaur-min-ondark.svg` |
| Horizontal lockup | `proxima-lockup-onlight.svg` | `proxima-lockup-ondark.svg` |
| Stacked lockup | `proxima-lockup-stacked-onlight.svg` | `proxima-lockup-stacked-ondark.svg` |

The dark variants differ in three values only — ink `#14161a` → `#e6e9ef`, the
Proxima star `#e5484d` → `#ff5c60`, constellation-line opacity `0.55` → `0.62`.
Regenerate rather than hand-edit:

```
sed -e 's/color="#14161a"/color="#e6e9ef"/' -e 's/#e5484d/#ff5c60/g' \
    -e 's/opacity="0.55"/opacity="0.62"/' -e 's/light backgrounds/dark backgrounds/' \
    proxima-lockup-onlight.svg > proxima-lockup-ondark.svg
```

Since everything is `currentColor`, an inlined variant takes the page's ink
either way. The variant choice decides what happens where there is no CSS to
inherit from: a favicon, an `<img src=…>`, the file opened on its own.

## Putting it on a page

The page carries two placeholders — `<!--LOGO-->` where the SVG is inlined and
`%FAVICON%` inside `<link rel="icon" href="…">` — and the handler fills them at
init:

```go
var somePage = logo.Page(someHTML, logo.LockupOnDark, logo.MarkOnDark)
```

The lockup in the page, the bare mark as the tab icon: at 16 px the wordmark is
illegible and the constellation is all that survives.

One CSS rule is always needed, and it has to sit on the `svg` element itself —
the file carries `width`, `height` and `color` presentation attributes for
standalone use, and a rule on a wrapper loses to them:

```css
#somewhere .brand svg { display: block; width: 150px; height: auto; color: #e0e0e0; }
```

Below about 120 px wide the wordmark's hairlines start to disappear; use the
mark alone instead of shrinking the lockup further.

## Where it is used

| Page | Inline | Favicon |
|------|--------|---------|
| `api/monitor` | mark (light) — the masthead already spells the name out | mark (light) |
| `api/dagviz` | horizontal lockup (light) | mark (light) |
| `api/dag_explorer` | horizontal lockup (dark) | mark (dark) |
| `api/chain_explorer` | horizontal lockup (dark) | mark (dark) |
| `api/server` netviz | horizontal lockup (dark) | mark (dark) |

The docs site keeps its own copies under `static/img/` — the mark as the
favicon, the horizontal lockup as the sidebar masthead. They are copies, so an
edit here has to be carried over by hand.
