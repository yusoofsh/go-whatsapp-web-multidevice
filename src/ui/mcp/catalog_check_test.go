package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// argumentGetters are CallToolRequest methods whose first argument is a key.
var argumentGetters = map[string]struct{}{
	"GetString": {}, "RequireString": {},
	"GetInt": {}, "RequireInt": {},
	"GetFloat": {}, "RequireFloat": {},
	"GetBool": {}, "RequireBool": {},
	"GetStringSlice": {}, "RequireStringSlice": {},
	"GetIntSlice": {}, "RequireIntSlice": {},
	"GetFloatSlice": {}, "RequireFloatSlice": {},
	"GetBoolSlice": {}, "RequireBoolSlice": {},
}

func TestHandlerArgumentKeysAreInPublishedSchema(t *testing.T) {
	// Copying is_forwarded into the poll request is a separate case.
	t.Log("poll branch is unchecked")
	catalogPath := publishedCatalogPath(t)
	t.Logf("published catalog %s", filepath.ToSlash(catalogPath))
	props := catalogProperties(t, catalogPath)
	reads := handlerArgumentReads(t)
	var missing []string
	var tools []string
	for tool := range reads {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	if len(tools) != 11 {
		t.Fatalf("expected argument reads for 11 tools, found %d: %s", len(tools), strings.Join(tools, ", "))
	}
	for _, tool := range tools {
		keys := reads[tool]
		if len(keys) == 0 {
			t.Errorf("%s has no handler argument reads", tool)
		}
		declared := props[tool]
		if declared == nil {
			t.Errorf("%s is not in docs/mcp-tools.json", tool)
			continue
		}
		for _, read := range keys {
			if _, ok := declared[read.key]; ok {
				continue
			}
			missing = append(missing, fmt.Sprintf("%s:%s missing=[%q]", read.file, read.fn, read.key))
		}
	}
	if _, ok := readSet(reads["whatsapp_message"])["link"]; !ok {
		t.Fatal("whatsapp_message handler read of link was not found")
	}
	if _, ok := readSet(reads["whatsapp_send"])["allow_reshare"]; !ok {
		t.Fatal("whatsapp_send handler read of allow_reshare was not found")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("handler argument keys missing from published schema:\n%s", strings.Join(missing, "\n"))
	}
}

func exportParityCatalog(t *testing.T) []byte {
	t.Helper()
	s := newParityTestSuite(t)
	result := invokeData(t, s.s, s.ctx, "tools/list", map[string]any{})
	raw, err := json.MarshalIndent(result["result"], "", "  ")
	require.NoError(t, err)
	return append(raw, '\n')
}

func requirePublishedCatalogMatch(t *testing.T, first []byte) {
	t.Helper()
	second := exportParityCatalog(t)
	if !bytes.Equal(first, second) {
		t.Fatal("export is not byte-identical across runs")
	}
	catalog := publishedCatalogPath(t)
	exportPath := filepath.Join(t.TempDir(), "mcp-tools.json")
	require.NoError(t, os.WriteFile(exportPath, first, 0644))
	cmd := exec.Command("git", "--no-pager", "diff", "--exit-code", "--no-index", "--", catalog, exportPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		t.Fatalf("git diff --exit-code docs/mcp-tools.json\n%s", out)
	}
	t.Fatalf("git diff --exit-code docs/mcp-tools.json: %v\n%s", err, out)
}

func publishedCatalogPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("catalog path unavailable")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "mcp-tools.json"))
	if !strings.HasSuffix(filepath.ToSlash(path), "docs/mcp-tools.json") {
		t.Fatalf("catalog path %s is not docs/mcp-tools.json", path)
	}
	return path
}

func catalogProperties(t *testing.T, path string) map[string]map[string]struct{} {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	out := make(map[string]map[string]struct{}, len(doc.Tools))
	for _, tool := range doc.Tools {
		set := make(map[string]struct{}, len(tool.InputSchema.Properties))
		for key := range tool.InputSchema.Properties {
			set[key] = struct{}{}
		}
		out[tool.Name] = set
	}
	return out
}

type argumentRead struct {
	file string
	fn   string
	key  string
}

type fnInfo struct {
	file  string
	keys  map[string]struct{}
	calls map[string]struct{}
}

func handlerArgumentReads(t *testing.T) map[string][]argumentRead {
	t.Helper()
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("mcp source path unavailable")
	}
	dir := filepath.Dir(caller)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, 0)
	require.NoError(t, err)
	pkg := pkgs["mcp"]
	require.NotNil(t, pkg)
	fns := map[string]*fnInfo{}
	roots := map[string][]string{}
	var files []string
	for name := range pkg.Files {
		files = append(files, name)
	}
	sort.Strings(files)
	for _, name := range files {
		file := pkg.Files[name]
		base := filepath.Base(name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, exists := fns[fn.Name.Name]; exists {
				t.Fatalf("duplicate function %s", fn.Name.Name)
			}
			info := &fnInfo{file: base, keys: map[string]struct{}{}, calls: map[string]struct{}{}}
			fns[fn.Name.Name] = info
			walkBlock(fn.Body, info, fns, base, roots)
		}
	}
	out := map[string][]argumentRead{}
	var tools []string
	for tool := range roots {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		seen := map[string]bool{}
		var reads []argumentRead
		for _, root := range roots[tool] {
			reads = append(reads, collectReads(root, fns, seen)...)
		}
		sort.Slice(reads, func(i, j int) bool {
			if reads[i].file != reads[j].file {
				return reads[i].file < reads[j].file
			}
			if reads[i].fn != reads[j].fn {
				return reads[i].fn < reads[j].fn
			}
			return reads[i].key < reads[j].key
		})
		out[tool] = reads
	}
	return out
}

func collectReads(name string, fns map[string]*fnInfo, seen map[string]bool) []argumentRead {
	if seen[name] {
		return nil
	}
	seen[name] = true
	info := fns[name]
	if info == nil {
		return nil
	}
	var reads []argumentRead
	keys := make([]string, 0, len(info.keys))
	for key := range info.keys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reads = append(reads, argumentRead{file: info.file, fn: name, key: key})
	}
	calls := make([]string, 0, len(info.calls))
	for call := range info.calls {
		calls = append(calls, call)
	}
	sort.Strings(calls)
	for _, call := range calls {
		reads = append(reads, collectReads(call, fns, seen)...)
	}
	return reads
}

func walkBlock(body *ast.BlockStmt, info *fnInfo, fns map[string]*fnInfo, file string, roots map[string][]string) {
	if body == nil {
		return
	}
	toolVars := map[string]string{}
	argMaps := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch n := n.(type) {
		case *ast.AssignStmt:
			captureAssign(n, toolVars, argMaps)
		case *ast.CallExpr:
			captureCall(n, info, fns, file, roots, toolVars)
		case *ast.IndexExpr:
			captureIndex(n, info, argMaps)
		}
		return true
	})
}

func captureAssign(n *ast.AssignStmt, toolVars map[string]string, argMaps map[string]bool) {
	if len(n.Lhs) != 1 || len(n.Rhs) != 1 {
		return
	}
	id, ok := n.Lhs[0].(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	if name, ok := newToolName(n.Rhs[0]); ok {
		toolVars[id.Name] = name
	}
	if isGetArguments(n.Rhs[0]) {
		argMaps[id.Name] = true
	}
}

func captureCall(n *ast.CallExpr, info *fnInfo, fns map[string]*fnInfo, file string, roots map[string][]string, toolVars map[string]string) {
	switch fun := n.Fun.(type) {
	case *ast.Ident:
		info.calls[fun.Name] = struct{}{}
		if fun.Name == "requiredText" && len(n.Args) >= 2 {
			if key, ok := stringLit(n.Args[1]); ok {
				info.keys[key] = struct{}{}
			}
		}
		if fun.Name == "addParityTool" && len(n.Args) >= 6 {
			tool, ok := stringLit(n.Args[1])
			fn, okFn := handlerName(n.Args[5], tool, file, fns, roots)
			if ok && okFn {
				addRoot(roots, tool, fn)
			}
		}
	case *ast.SelectorExpr:
		info.calls[fun.Sel.Name] = struct{}{}
		if _, ok := argumentGetters[fun.Sel.Name]; ok && len(n.Args) >= 1 {
			if key, ok := stringLit(n.Args[0]); ok {
				info.keys[key] = struct{}{}
			}
		}
		if fun.Sel.Name == "AddTool" && len(n.Args) >= 2 {
			id, ok := n.Args[0].(*ast.Ident)
			if !ok {
				return
			}
			tool := toolVars[id.Name]
			fn, okFn := handlerName(n.Args[1], tool, file, fns, roots)
			if tool != "" && okFn {
				addRoot(roots, tool, fn)
			}
		}
	}
}

func captureIndex(n *ast.IndexExpr, info *fnInfo, argMaps map[string]bool) {
	key, ok := stringLit(n.Index)
	if !ok {
		return
	}
	if id, ok := n.X.(*ast.Ident); ok && argMaps[id.Name] {
		info.keys[key] = struct{}{}
		return
	}
	if isGetArguments(n.X) {
		info.keys[key] = struct{}{}
	}
}

func handlerName(expr ast.Expr, tool, file string, fns map[string]*fnInfo, roots map[string][]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name, true
	case *ast.Ident:
		return e.Name, true
	case *ast.FuncLit:
		name := tool + "#handler"
		if tool == "" {
			return "", false
		}
		if _, exists := fns[name]; exists {
			return "", false
		}
		info := &fnInfo{file: file, keys: map[string]struct{}{}, calls: map[string]struct{}{}}
		fns[name] = info
		walkBlock(e.Body, info, fns, file, roots)
		return name, true
	default:
		return "", false
	}
}

func addRoot(roots map[string][]string, tool, fn string) {
	for _, existing := range roots[tool] {
		if existing == fn {
			return
		}
	}
	roots[tool] = append(roots[tool], fn)
}

func newToolName(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewTool" {
		return "", false
	}
	return stringLit(call.Args[0])
}

func isGetArguments(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "GetArguments"
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

func readSet(reads []argumentRead) map[string]struct{} {
	out := make(map[string]struct{}, len(reads))
	for _, read := range reads {
		out[read.key] = struct{}{}
	}
	return out
}
