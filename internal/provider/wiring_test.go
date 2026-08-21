package provider

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// declKey names a function for the little call graph below: "Type.Method" for a
// method, or the bare name for a plain function.
func declKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return receiverType(fn) + "." + fn.Name.Name
}

func receiverType(fn *ast.FuncDecl) string {
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// receiverName is the identifier the method uses for itself, "r" by convention.
func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

// parseResourceDecls reads every non-test *_resource.go in the package.
func parseResourceDecls(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	entries, err := filepath.Glob("*_resource.go")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no resource sources found: %v", err)
	}
	fset := token.NewFileSet()
	decls := make(map[string]*ast.FuncDecl)
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, d := range file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				decls[declKey(fn)] = fn
			}
		}
	}
	return decls
}

// callsFrom lists what a declaration calls: plain functions by name, and its own
// methods as "Type.Method" so the walk can follow one resource's delegation.
func callsFrom(fn *ast.FuncDecl) []string {
	self, recv := receiverType(fn), receiverName(fn)
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			out = append(out, f.Name)
		case *ast.SelectorExpr:
			if id, ok := f.X.(*ast.Ident); ok && id.Name == recv {
				out = append(out, self+"."+f.Sel.Name)
			}
		}
		return true
	})
	return out
}

// findCaller returns the declaration that actually contains the revalidatePlan
// call, following delegation from the entry point.
func findCaller(decls map[string]*ast.FuncDecl, entry string) *ast.FuncDecl {
	seen := map[string]bool{}
	queue := []string{entry}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		fn := decls[key]
		if fn == nil {
			continue
		}
		for _, callee := range callsFrom(fn) {
			if callee == "revalidatePlan" {
				return fn
			}
			queue = append(queue, callee)
		}
	}
	return nil
}

// firstAPICallPos is the position of the first call made THROUGH one of the
// receiver's own fields — r.models.Enable, r.notifiers.Create — which is the
// earliest point at which the resource can touch the server.
func firstAPICallPos(fn *ast.FuncDecl) token.Pos {
	recv := receiverName(fn)
	first := token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		field, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := field.X.(*ast.Ident); !ok || id.Name != recv {
			return true
		}
		if first == token.NoPos || call.Pos() < first {
			first = call.Pos()
		}
		return true
	})
	return first
}

func revalidatePos(fn *ast.FuncDecl) token.Pos {
	pos := token.NoPos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "revalidatePlan" && pos == token.NoPos {
			pos = call.Pos()
		}
		return true
	})
	return pos
}

// TestEveryResourceRevalidatesThePlan is the wiring guarantee. revalidatePlan
// only protects a resource that actually calls it, and a resource that dropped
// the call — or a new resource that never added it — failed nothing: every other
// test drives the helper directly. This reads the sources instead, and takes the
// list of resources from the provider's own registry, so a resource cannot be
// added without being covered.
func TestEveryResourceRevalidatesThePlan(t *testing.T) {
	ctx := context.Background()
	decls := parseResourceDecls(t)

	resources := New("test")().Resources(ctx)
	if len(resources) == 0 {
		t.Fatal("no resources registered")
	}
	for _, f := range resources {
		typeName := reflect.TypeOf(f()).Elem().Name()
		for _, method := range []string{"Create", "Update"} {
			entry := typeName + "." + method
			t.Run(entry, func(t *testing.T) {
				if decls[entry] == nil {
					t.Fatalf("%s not found in the parsed sources", entry)
				}
				caller := findCaller(decls, entry)
				if caller == nil {
					t.Fatalf("%s never reaches revalidatePlan", entry)
				}
				api := firstAPICallPos(caller)
				if api == token.NoPos {
					return // the write happens in a callee; reaching the call is the guarantee
				}
				if pos := revalidatePos(caller); pos > api {
					t.Errorf("%s calls the API before revalidating the plan (in %s)", entry, declKey(caller))
				}
			})
		}
	}
}
