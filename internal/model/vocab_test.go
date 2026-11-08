package model

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unsaid are words a user never reads: persisted or wire names the UI maps
// to its own words, and other platforms' vocabulary.
var unsaid = regexp.MustCompile(`(?i)\bdfw\b|\bdrs\b|segment|\btier|port ?group|port-group|\bgateway\b|distributed firewall|\bnsx|\bvds\b|vmotion|vcenter|vsphere|esxi`)

// says is the unsaid word text carries, if any. A route's default gateway is
// the routing term, not the firewall.
func says(text string) string {
	return unsaid.FindString(strings.ReplaceAll(strings.ToLower(text), "default gateway", ""))
}

// raisers build the text an error, a refusal or a trace note carries to a
// response.
var raisers = map[string]bool{
	"fmt.Errorf": true, "errors.New": true, "http.Error": true, "invalid": true, "unavailable": true,
	"s.platformScopeWith": true, "w.addStep": true, "w.maybe": true,
}

// shown are the response fields whose literal text the UI renders as is.
var shown = map[string]bool{"Note": true, "Warning": true, "Message": true, "Field": true, "CreateLabel": true, "EditLabel": true, "Noun": true, "Enables": true}

// wire are the persisted and wire values passed beside such text, which the
// UI maps to its own words.
var wire = regexp.MustCompile(`^"(dfw|segment|gateway|drs)"$`)

// Error text, trace notes and change labels reach the UI verbatim, so no
// message the server builds may carry an unsaid word. This walks every
// non-test source for the literals they are made from.
func TestErrorTextSaysOnlyTheUIsWords(t *testing.T) {
	fset := token.NewFileSet()
	for _, root := range []string{"../../internal", "../../pkg", "../../cmd"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if kv, ok := n.(*ast.KeyValueExpr); ok {
					key, _ := kv.Key.(*ast.Ident)
					lit, _ := kv.Value.(*ast.BasicLit)
					if key != nil && shown[key.Name] && lit != nil && lit.Kind == token.STRING && says(lit.Value) != "" {
						t.Errorf("%s: %s %s carries %q", fset.Position(lit.Pos()), key.Name, lit.Value, says(lit.Value))
					}
					return true
				}
				call, ok := n.(*ast.CallExpr)
				if !ok || !raisers[callee(call)] {
					return true
				}
				for _, a := range call.Args {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING && !wire.MatchString(lit.Value) && says(lit.Value) != "" {
						t.Errorf("%s: text %s carries %q", fset.Position(lit.Pos()), lit.Value, says(lit.Value))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	case *ast.Ident:
		return fn.Name
	}
	return ""
}
