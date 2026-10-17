package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type hit struct {
	path string
	line int
}

func scan(dir string) ([]hit, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "event_*.go"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%s has no event_*.go files", dir)
	}
	sort.Strings(matches)

	var hits []hit
	fset := token.NewFileSet()
	for _, path := range matches {
		fileHits, err := scanFile(fset, path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for i := range fileHits {
			fileHits[i].path = relPath(dir, fileHits[i].path)
		}
		hits = append(hits, fileHits...)
	}
	return hits, nil
}

func scanFile(fset *token.FileSet, path string) ([]hit, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	pkgName, dot := contextImport(file)
	if pkgName == "" && !dot {
		return nil, nil
	}

	var stmts []*ast.GoStmt
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.GoStmt)
		if ok {
			stmts = append(stmts, stmt)
		}
		return true
	})

	var hits []hit
	for _, stmt := range stmts {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if n != stmt {
				if _, nested := n.(*ast.GoStmt); nested {
					return false
				}
			}
			if isBackgroundCall(n, pkgName, dot) {
				hits = append(hits, hit{
					path: path,
					line: fset.Position(n.Pos()).Line,
				})
			}
			return true
		})
	}
	return hits, nil
}

func contextImport(file *ast.File) (name string, dot bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "context" {
			continue
		}
		if spec.Name == nil {
			return "context", false
		}
		switch spec.Name.Name {
		case ".":
			return "", true
		case "_":
			return "", false
		default:
			return spec.Name.Name, false
		}
	}
	return "", false
}

func isBackgroundCall(n ast.Node, pkgName string, dot bool) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || call.Ellipsis.IsValid() {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if dot || fun.Sel == nil || fun.Sel.Name != "Background" {
			return false
		}
		id, ok := fun.X.(*ast.Ident)
		return ok && id.Name == pkgName
	case *ast.Ident:
		return dot && fun.Name == "Background"
	default:
		return false
	}
}

func relPath(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
