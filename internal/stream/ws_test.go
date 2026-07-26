package stream

import (
	"net/http"
	"testing"
)

func req(origin, host string) *http.Request {
	r := &http.Request{Host: host, Header: http.Header{}}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestCheckOrigin(t *testing.T) {
	SetAllowedOrigin("http://localhost:5173")
	t.Cleanup(func() { SetAllowedOrigin("") })

	cases := []struct {
		name         string
		origin, host string
		want         bool
	}{
		{"no origin (non-browser)", "", "dotvirt.example", true},
		{"configured UI origin", "http://localhost:5173", "dotvirt.example", true},
		{"same-origin", "https://dotvirt.example", "dotvirt.example", true},
		{"foreign origin", "https://evil.example", "dotvirt.example", false},
		{"foreign origin spoofing host in path", "https://evil.example/dotvirt.example", "dotvirt.example", false},
		{"unparseable origin", "::::", "dotvirt.example", false},
	}
	for _, c := range cases {
		if got := checkOrigin(req(c.origin, c.host)); got != c.want {
			t.Errorf("%s: checkOrigin(origin=%q,host=%q)=%v want %v", c.name, c.origin, c.host, got, c.want)
		}
	}
}

func TestCheckOriginSameOriginOnlyWhenUnset(t *testing.T) {
	SetAllowedOrigin("")
	if checkOrigin(req("http://localhost:5173", "dotvirt.example")) {
		t.Error("with no allowed origin configured, a non-same-origin must be rejected")
	}
	if !checkOrigin(req("https://dotvirt.example", "dotvirt.example")) {
		t.Error("same-origin must always be allowed")
	}
}
