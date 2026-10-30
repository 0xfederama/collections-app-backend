// Package archtest enforces the layer rules of F-019 that golangci-lint's depguard
// cannot express across files: handler -> service -> repository -> database, one
// direction, and no reaching into another feature's handler or repository.
// The per-file rules live in .golangci.yml; keep the two in step.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/0xfederama/collections-app-backend"
	// moduleRoot is relative to this package directory (go test runs there).
	moduleRoot = "../../.."
	sqlcPath   = modulePath + "/internal/platform/database/sqlc"
	humaPrefix = "github.com/danielgtaylor/huma"
)

// legacyPackages are not migrated to the layers yet. They are exempt from every
// check (a prefix covers subpackages). B3b..f remove their entries as they migrate.
var legacyPackages = []string{
	"internal/trash",   // migrates in B3b..f
	"internal/search",  // migrates in B3b..f
	"internal/images",  // migrates in B3b..f
	"internal/lookup",  // migrates in B3b..f
	"internal/httpapi", // migrates in B3b..f
	"internal/seed",    // migrates in B3b..f
	"cmd/api",          // migrates in B3b..f
}

// sqlcExemptPackages may import the sqlc package from any file: the database
// plumbing itself, the seeding code and the test database helper.
var sqlcExemptPackages = []string{
	"internal/seed",
	"internal/platform/testdb",
	"internal/platform/database",
}

// sqlcExemptFiles may import sqlc outside repository.go. bootstrap.go runs the
// startup seeding of a new workspace in one transaction (see workspace.Bootstrap).
var sqlcExemptFiles = []string{
	"internal/workspace/bootstrap.go",
}

type sourceFile struct {
	path string // slash-separated, relative to the module root
	pkg  string // package directory, relative to the module root
	ast  *ast.File
}

type loaded struct {
	fset  *token.FileSet
	files []sourceFile
}

func TestLayerRules(t *testing.T) {
	mod := loadModule(t)

	// Guard against a wrong root: the walk must see a migrated feature.
	if !hasFile(mod.files, "internal/workspace/handler.go") {
		t.Fatal("module walk did not find internal/workspace/handler.go; check moduleRoot")
	}

	t.Run("sqlc is imported only by repository.go", func(t *testing.T) {
		for _, v := range sqlcViolations(mod) {
			t.Error(v)
		}
	})
	t.Run("service.go and model.go know no HTTP", func(t *testing.T) {
		for _, v := range httpViolations(mod) {
			t.Error(v)
		}
	})
	t.Run("features do not use each other's handler or repository identifiers", func(t *testing.T) {
		for _, v := range crossFeatureViolations(mod) {
			t.Error(v)
		}
	})
	t.Run("legacy allowlist names existing packages", func(t *testing.T) {
		dirs := map[string]bool{}
		for _, f := range mod.files {
			dirs[f.pkg] = true
		}
		for _, p := range legacyPackages {
			if !dirs[p] {
				t.Errorf("legacy package %s has no Go files; remove its entry", p)
			}
		}
	})
}

// loadModule parses every .go file of the module (tests included), skipping hidden,
// testdata and vendor directories.
func loadModule(t *testing.T) loaded {
	t.Helper()
	out := loaded{fset: token.NewFileSet()}
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != moduleRoot && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(out.fset, path, nil, 0)
		if err != nil {
			return err
		}
		out.files = append(out.files, sourceFile{
			path: rel,
			pkg:  filepath.ToSlash(filepath.Dir(rel)),
			ast:  f,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	return out
}

func hasFile(files []sourceFile, path string) bool {
	for _, f := range files {
		if f.path == path {
			return true
		}
	}
	return false
}

// underDir reports whether pkg is dir or a package below it.
func underDir(pkg, dir string) bool {
	return pkg == dir || strings.HasPrefix(pkg, dir+"/")
}

func isLegacy(pkg string) bool {
	for _, p := range legacyPackages {
		if underDir(pkg, p) {
			return true
		}
	}
	return false
}

func importPath(spec *ast.ImportSpec) string {
	p, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return spec.Path.Value
	}
	return p
}

// sqlcViolations: only repository.go (plus the exempt packages and files) may import sqlc.
func sqlcViolations(mod loaded) []string {
	var out []string
	for _, f := range mod.files {
		if isLegacy(f.pkg) || filepath.Base(f.path) == "repository.go" {
			continue
		}
		if exemptFromSQLC(f) {
			continue
		}
		for _, imp := range f.ast.Imports {
			if importPath(imp) == sqlcPath {
				out = append(out, f.path+" imports "+sqlcPath+"; only repository.go may (layer rule)")
			}
		}
	}
	return out
}

// httpViolations: service.go and model.go import neither net/http nor huma.
func httpViolations(mod loaded) []string {
	var out []string
	for _, f := range mod.files {
		base := filepath.Base(f.path)
		if isLegacy(f.pkg) || (base != "service.go" && base != "model.go") {
			continue
		}
		for _, imp := range f.ast.Imports {
			p := importPath(imp)
			if p == "net/http" || strings.HasPrefix(p, humaPrefix) {
				out = append(out, f.path+" imports "+p+"; "+base+" must know no HTTP (layer rule)")
			}
		}
	}
	return out
}

// feature is a package with a handler.go or repository.go: its exported
// top-level identifiers are what other features must not use.
type feature struct {
	name string
	ids  map[string]bool
}

// crossFeatureViolations: a feature package never references an exported handler.go
// or repository.go identifier of another feature (e.g. workspace.Repo, workspace.Register).
func crossFeatureViolations(mod loaded) []string {
	features := map[string]*feature{}
	for _, f := range mod.files {
		base := filepath.Base(f.path)
		if isLegacy(f.pkg) || (base != "handler.go" && base != "repository.go") {
			continue
		}
		ft := features[f.pkg]
		if ft == nil {
			ft = &feature{name: f.ast.Name.Name, ids: map[string]bool{}}
			features[f.pkg] = ft
		}
		for _, decl := range f.ast.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					ft.ids[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						ft.ids[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							ft.ids[n.Name] = true
						}
					}
				}
			}
		}
	}

	var out []string
	for _, f := range mod.files {
		if isLegacy(f.pkg) {
			continue
		}
		for _, imp := range f.ast.Imports {
			target, ok := internalPackage(importPath(imp))
			if !ok || target == f.pkg {
				continue
			}
			ft, ok := features[target]
			if !ok {
				continue
			}
			if imp.Name != nil && imp.Name.Name == "." {
				out = append(out, f.path+" dot-imports "+target+"; it must be used qualified (layer rule)")
				continue
			}
			alias := ft.name
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			if alias == "_" {
				continue
			}
			ast.Inspect(f.ast, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, ok := sel.X.(*ast.Ident)
				if ok && x.Name == alias && ft.ids[sel.Sel.Name] {
					out = append(out, f.path+" uses "+alias+"."+sel.Sel.Name+" from another feature's handler/repository (layer rule)")
				}
				return true
			})
		}
	}
	return out
}

// internalPackage maps an import path inside this module to its package directory.
func internalPackage(imp string) (string, bool) {
	rest, ok := strings.CutPrefix(imp, modulePath+"/")
	if !ok || !strings.HasPrefix(rest, "internal/") {
		return "", false
	}
	return rest, true
}

func exemptFromSQLC(f sourceFile) bool {
	if slices.Contains(sqlcExemptFiles, f.path) {
		return true
	}
	for _, dir := range sqlcExemptPackages {
		if underDir(f.pkg, dir) {
			return true
		}
	}
	return false
}
