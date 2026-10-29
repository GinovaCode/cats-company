package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Gate-only convenience builders support registry tests. A new delivery path
// must not accidentally use that fallback and issue explicit grants to an old
// client. Inspect production callers rather than maintaining a caller allowlist.
func TestLiveDeviceMetadataCallersRequireNegotiation(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate server sources")
	}
	files, err := os.ReadDir(filepath.Dir(source))
	if err != nil {
		t.Fatal(err)
	}
	convenience := map[string]bool{"buildXiaobaRuntimeMetadata": true, "turnContext": true, "turnContextForDevices": true, "turnContextForOwnerDevices": true}
	for _, entry := range files {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(source), entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if convenience[selector.Sel.Name] && !convenience[fn.Name.Name] {
					t.Errorf("%s: production caller must use an explicitly negotiated builder", fset.Position(call.Pos()))
				}
				if selector.Sel.Name != "buildCatscoIdentityMetadata" {
					return true
				}
				guarded := false
				for _, arg := range call.Args {
					options, ok := arg.(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, element := range options.Elts {
						field, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := field.Key.(*ast.Ident)
						if !ok {
							continue
						}
						if key.Name == "OmitDeviceAccess" {
							value, ok := field.Value.(*ast.Ident)
							guarded = guarded || (ok && value.Name == "true")
						}
						if key.Name == "ExplicitTargetsEnabled" {
							value, ok := field.Value.(*ast.UnaryExpr)
							guarded = guarded || (ok && value.Op == token.AND)
						}
					}
				}
				if !guarded {
					t.Errorf("%s: supply the negotiated capability or omit executable device access", fset.Position(call.Pos()))
				}
				return true
			})
		}
	}
}
