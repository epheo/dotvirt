package controller

import (
	"regexp"
	"strings"
)

// argoMatch matches s the way Argo CD matches its destinationServiceAccounts
// and application namespaces: a glob whose * crosses every character
// (gobwas/glob without separators: * ? [..] [!..] {a,b} \), or, where regex
// allows, a /regexp/ matched anywhere in s. A pattern that does not compile
// matches nothing, as Argo refuses it.
func argoMatch(pattern, s string, regex bool) bool {
	if regex && len(pattern) > 1 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		re, err := regexp.Compile(pattern[1 : len(pattern)-1])
		return err == nil && re.MatchString(s)
	}
	re, err := regexp.Compile(globRegexp(pattern))
	return err == nil && re.MatchString(s)
}

func globRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	inClass, alternation := false, 0
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			i++
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		case inClass:
			if c == ']' {
				inClass = false
			}
			b.WriteByte(c)
		case c == '[':
			inClass = true
			b.WriteByte('[')
			if i+1 < len(pattern) && pattern[i+1] == '!' {
				b.WriteByte('^')
				i++
			}
		case c == '*':
			b.WriteString(".*")
		case c == '?':
			b.WriteByte('.')
		case c == '{':
			alternation++
			b.WriteString("(?:")
		case c == '}' && alternation > 0:
			alternation--
			b.WriteByte(')')
		case c == ',' && alternation > 0:
			b.WriteByte('|')
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	return b.String()
}
