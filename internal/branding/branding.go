// Package branding dresses the console as a provider's: a product name,
// masthead links, and a stylesheet and logo dotvirt serves itself from a
// mounted directory, never from a remote URL, so the console's
// content-security policy holds as it is.
package branding

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/epheo/dotvirt/internal/model"
)

// The bounds a provider's branding keeps to: a masthead fits them, and no
// file read on a page load grows past MaxFile.
const (
	MaxName  = 40
	MaxLinks = 5
	MaxLabel = 32
	MaxFile  = 256 << 10
)

// The files a branding directory may hold; any other is never read.
const (
	ThemeFile = "theme.css"
	LogoSVG   = "logo.svg"
	LogoPNG   = "logo.png"
)

// Settings are a provider's branding; the zero value is the console's own.
type Settings struct {
	ProductName string
	Links       []model.BrandingLink
	// Dir holds ThemeFile and a logo, a mounted ConfigMap; "" for none.
	Dir string
}

// CheckName refuses a product name the masthead cannot carry as plain text.
func CheckName(name string) error {
	if utf8.RuneCountInString(name) > MaxName {
		return fmt.Errorf("at most %d characters", MaxName)
	}
	if !printable(name) {
		return errors.New("printable characters only")
	}
	return nil
}

// ParseLinks reads a JSON list of {label, url}: at most MaxLinks, each label
// plain text of at most MaxLabel characters, each URL https with a host and
// no credentials.
func ParseLinks(raw string) ([]model.BrandingLink, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var links []model.BrandingLink
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&links); err != nil {
		return nil, fmt.Errorf("not a JSON list of {label, url}: %v", err)
	}
	if len(links) > MaxLinks {
		return nil, fmt.Errorf("at most %d links", MaxLinks)
	}
	for _, l := range links {
		if n := utf8.RuneCountInString(l.Label); n == 0 || n > MaxLabel || !printable(l.Label) {
			return nil, fmt.Errorf("link label %q: 1 to %d printable characters", l.Label, MaxLabel)
		}
		u, err := url.Parse(l.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(l.URL, " \t\n") {
			return nil, fmt.Errorf("link %q: an https URL with a host and no credentials", l.URL)
		}
	}
	return links, nil
}

// printable refuses control characters, C0, DEL and C1: the rule the CRD's
// pattern states, so admission and startup agree.
func printable(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }) < 0
}

// Info is what the console asks before anyone signs in: the login screen
// wears the branding too.
func (s Settings) Info() model.Branding {
	b := model.Branding{ProductName: s.ProductName, Links: s.Links}
	b.Theme = s.present(ThemeFile)
	b.Logo = s.present(LogoSVG) || s.present(LogoPNG)
	return b
}

// ShellID is the id of the branding block Shell writes, which the console
// reads at init in place of GET /api/branding.
const ShellID = "dotvirt-branding"

// Shell is the console's index.html dressed for its first paint: the product
// name as its title, the stylesheet after the console's own, and Info as a
// JSON block. None of them is a script the browser runs, so the
// content-security policy derived from the plain index still holds. Called per
// page load, like Info: a ConfigMap change shows on the next one. An index
// with no </head> is returned as it is.
func (s Settings) Shell(index []byte) []byte {
	at := bytes.Index(index, []byte("</head>"))
	if at < 0 {
		return index
	}
	info := s.Info()
	var b bytes.Buffer
	fmt.Fprintf(&b, "<title>%s</title>", html.EscapeString(cmp.Or(info.ProductName, "dotvirt")))
	if info.Logo {
		// Declared after the bundled icon, so the browser takes this one.
		b.WriteString(`<link rel="icon" href="/api/branding/logo">`)
	}
	if info.Theme {
		b.WriteString(`<link rel="stylesheet" href="/api/branding/theme.css">`)
	}
	// Marshal escapes <, > and &, so no value can close the block.
	raw, _ := json.Marshal(info)
	fmt.Fprintf(&b, `<script type="application/json" id="%s">%s</script>`, ShellID, raw)
	out := make([]byte, 0, len(index)+b.Len())
	out = append(out, index[:at]...)
	out = append(out, b.Bytes()...)
	return append(out, index[at:]...)
}

// present reports whether read would answer name, without reading it: each
// page load asks this.
func (s Settings) present(name string) bool {
	if s.Dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(s.Dir, name))
	return err == nil && fi.Mode().IsRegular() && fi.Size() <= MaxFile
}

// ServeTheme answers the stylesheet, loaded after the console's own so it
// may override its tokens.
func (s Settings) ServeTheme(w http.ResponseWriter, r *http.Request) {
	content, ok := s.read(ThemeFile)
	if !ok {
		http.NotFound(w, r)
		return
	}
	serve(w, r, "text/css; charset=utf-8", content)
}

// ServeLogo answers the logo. An SVG is a document that may carry script, so
// one opened directly runs sandboxed, with nothing to load.
func (s Settings) ServeLogo(w http.ResponseWriter, r *http.Request) {
	name, content, ok := s.logo()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if name == LogoSVG {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		serve(w, r, "image/svg+xml", content)
		return
	}
	serve(w, r, "image/png", content)
}

func (s Settings) logo() (string, []byte, bool) {
	for _, name := range []string{LogoSVG, LogoPNG} {
		if content, ok := s.read(name); ok {
			return name, content, true
		}
	}
	return "", nil, false
}

// read is name's bytes from Dir, refused past MaxFile. A mounted ConfigMap
// updates in place, so each read sees its current content.
func (s Settings) read(name string) ([]byte, bool) {
	if s.Dir == "" {
		return nil, false
	}
	f, err := os.Open(filepath.Join(s.Dir, name))
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	// Stat after Open follows the link: kubelet mounts each ConfigMap key as
	// a symlink into ..data/, so an Lstat check would refuse every real file.
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	content, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil || len(content) > MaxFile {
		return nil, false
	}
	return content, true
}

// serve answers content revalidated by its hash: a ConfigMap change shows
// on the next load, an unchanged file costs a 304.
func serve(w http.ResponseWriter, r *http.Request, contentType string, content []byte) {
	sum := sha256.Sum256(content)
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(content))
}
