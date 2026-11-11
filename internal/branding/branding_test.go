package branding

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// A product name is plain text the masthead can carry.
func TestCheckName(t *testing.T) {
	for name, ok := range map[string]bool{
		"":                      true,
		"Uniwatt Cloud":         true,
		strings.Repeat("a", 40): true,
		strings.Repeat("a", 41): false,
		"Uni\nwatt":             false,
		"Uni\x7fwatt":           false,
	} {
		if err := CheckName(name); (err == nil) != ok {
			t.Errorf("CheckName(%q) = %v, want ok %v", name, err, ok)
		}
	}
}

// Masthead links are few, labeled in plain text, and https with no
// credentials.
func TestParseLinks(t *testing.T) {
	links, err := ParseLinks(`[{"label":"Account","url":"https://c.unw.fr/account"},{"label":"Billing","url":"https://c.unw.fr/billing?tab=open#due"}]`)
	if err != nil || len(links) != 2 || links[0] != (model.BrandingLink{Label: "Account", URL: "https://c.unw.fr/account"}) {
		t.Fatalf("ParseLinks = %+v, %v", links, err)
	}
	if links, err := ParseLinks(" "); err != nil || links != nil {
		t.Errorf("empty: %+v, %v", links, err)
	}
	six := "[" + strings.TrimSuffix(strings.Repeat(`{"label":"a","url":"https://h"},`, 6), ",") + "]"
	for _, bad := range []string{
		six,
		`[{"label":"a","url":"http://h"}]`,
		`[{"label":"a","url":"javascript:alert(1)"}]`,
		`[{"label":"a","url":"https://u:p@h"}]`,
		`[{"label":"a","url":"https:///path"}]`,
		`[{"label":"","url":"https://h"}]`,
		`[{"label":"` + strings.Repeat("a", 33) + `","url":"https://h"}]`,
		`[{"label":"a\nb","url":"https://h"}]`,
		`[{"label":"a","url":"https://h","target":"_self"}]`,
		`{"label":"a","url":"https://h"}`,
	} {
		if _, err := ParseLinks(bad); err == nil {
			t.Errorf("ParseLinks(%s) accepted", bad)
		}
	}
}

func dir(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func get(h http.HandlerFunc, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// The stylesheet is served as CSS, revalidated by its hash; none, one past
// the size cap, or a directory by that name answers 404.
func TestServeTheme(t *testing.T) {
	s := Settings{Dir: dir(t, map[string]string{ThemeFile: ":root{--color-accent:#0a7}"})}
	if !s.Info().Theme {
		t.Error("Info says no theme")
	}
	w := get(s.ServeTheme)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/css; charset=utf-8" || w.Body.String() != ":root{--color-accent:#0a7}" {
		t.Fatalf("theme = %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if again := get(s.ServeTheme, "If-None-Match", etag); etag == "" || again.Code != http.StatusNotModified {
		t.Errorf("revalidation with %q = %d, want 304", etag, again.Code)
	}
	for name, s := range map[string]Settings{
		"no dir":  {},
		"missing": {Dir: t.TempDir()},
		"too big": {Dir: dir(t, map[string]string{ThemeFile: strings.Repeat("a", MaxFile+1)})},
	} {
		if w := get(s.ServeTheme); w.Code != http.StatusNotFound || s.Info().Theme {
			t.Errorf("%s: %d, theme %v; want 404 and none", name, w.Code, s.Info().Theme)
		}
	}
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, ThemeFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if w := get(Settings{Dir: d}.ServeTheme); w.Code != http.StatusNotFound {
		t.Errorf("a directory named %s: %d, want 404", ThemeFile, w.Code)
	}
}

// An SVG logo is served sandboxed, with nothing to load; a PNG as an image.
func TestServeLogo(t *testing.T) {
	svg := Settings{Dir: dir(t, map[string]string{LogoSVG: "<svg/>", LogoPNG: "png"})}
	w := get(svg.ServeLogo)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Body.String() != "<svg/>" {
		t.Errorf("svg logo = %d %q csp %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Security-Policy"))
	}
	png := Settings{Dir: dir(t, map[string]string{LogoPNG: "png"})}
	if w := get(png.ServeLogo); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !png.Info().Logo {
		t.Errorf("png logo = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(Settings{}.ServeLogo); w.Code != http.StatusNotFound {
		t.Errorf("no logo = %d, want 404", w.Code)
	}
}

// Kubelet mounts each ConfigMap key as a symlink into a ..data directory,
// itself a symlink swapped on update: the files behind the links are served.
func TestServesAMountedConfigMap(t *testing.T) {
	d := t.TempDir()
	stamp := filepath.Join(d, "..2026_11_11_00_00_00.000000001")
	if err := os.Mkdir(stamp, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{ThemeFile: ":root{}", LogoPNG: "png"} {
		if err := os.WriteFile(filepath.Join(stamp, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Base(stamp), filepath.Join(d, "..data")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ThemeFile, LogoPNG} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(d, name)); err != nil {
			t.Fatal(err)
		}
	}
	s := Settings{Dir: d}
	if info := s.Info(); !info.Theme || !info.Logo {
		t.Errorf("Info = %+v, want theme and logo", info)
	}
	if w := get(s.ServeTheme); w.Code != http.StatusOK || w.Body.String() != ":root{}" {
		t.Errorf("theme behind a symlink = %d %q", w.Code, w.Body.String())
	}
	if w := get(s.ServeLogo); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("logo behind a symlink = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}
