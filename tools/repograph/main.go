// repograph builds an in-memory structural graph of the Go workspace (AST
// parsing, no embeddings) and answers queries about it. It exists so an AI
// agent can orient itself in the codebase for ~1-2K tokens instead of reading
// whole files: shake → package → symbol → then Read only the exact line ranges
// reported here.
//
// The repo is small, so every invocation re-parses from scratch (<200ms);
// there is no cache and therefore no staleness.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ─── graph model ─────────────────────────────────────────────────────────────

type Symbol struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // func | method | struct | interface | type
	Recv      string   `json:"recv,omitempty"`
	Signature string   `json:"signature"`
	Doc       string   `json:"doc,omitempty"`
	File      string   `json:"file"` // repo-relative
	StartLine int      `json:"start"`
	EndLine   int      `json:"end"`
	Exported  bool     `json:"exported"`
	Calls     []string `json:"calls,omitempty"` // resolved workspace callees ("pkg.Func", "pkg.Recv.Method")
}

type Package struct {
	Module     string    `json:"module"` // module dir name, e.g. consent-service
	ImportPath string    `json:"import_path"`
	Dir        string    `json:"dir"` // repo-relative
	Doc        string    `json:"doc,omitempty"`
	Files      []string  `json:"files"`
	LOC        int       `json:"loc"`
	Imports    []string  `json:"imports,omitempty"` // workspace-internal only
	Symbols    []*Symbol `json:"symbols"`
}

type Endpoint struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type Graph struct {
	Root      string      `json:"root"`
	Packages  []*Package  `json:"packages"`
	Endpoints []*Endpoint `json:"endpoints"`
}

// symbol id used in call edges: "<pkg dir>:<Recv.>Name"
func symID(pkgDir, recv, name string) string {
	if recv != "" {
		return pkgDir + ":" + recv + "." + name
	}
	return pkgDir + ":" + name
}

// ─── workspace discovery ─────────────────────────────────────────────────────

// workspaceModules parses go.work "use" directives, returning module dirs
// (repo-relative), excluding this tool itself.
func workspaceModules(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		return nil, fmt.Errorf("read go.work: %w", err)
	}
	var mods []string
	inUse := false
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "use ("):
			inUse = true
		case inUse && l == ")":
			inUse = false
		case inUse && l != "":
			mods = append(mods, strings.TrimPrefix(l, "./"))
		case strings.HasPrefix(l, "use "):
			mods = append(mods, strings.TrimPrefix(strings.TrimPrefix(l, "use "), "./"))
		}
	}
	var out []string
	for _, m := range mods {
		if !strings.HasPrefix(m, "tools/") {
			out = append(out, m)
		}
	}
	return out, nil
}

func modulePath(root, mod string) string {
	data, err := os.ReadFile(filepath.Join(root, mod, "go.mod"))
	if err != nil {
		return mod
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return mod
}

// ─── parsing ─────────────────────────────────────────────────────────────────

func buildGraph(root string) (*Graph, error) {
	mods, err := workspaceModules(root)
	if err != nil {
		return nil, err
	}
	modPaths := map[string]string{} // module dir -> module import path
	for _, m := range mods {
		modPaths[m] = modulePath(root, m)
	}

	g := &Graph{Root: root}
	fset := token.NewFileSet()
	pkgByDir := map[string]*Package{}

	for _, mod := range mods {
		modRoot := filepath.Join(root, mod)
		err := filepath.WalkDir(modRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			dir := filepath.Dir(rel)

			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: parse %s: %v\n", rel, err)
				return nil
			}

			pkg, ok := pkgByDir[dir]
			if !ok {
				relInMod, _ := filepath.Rel(mod, dir)
				ip := modPaths[mod]
				if relInMod != "." {
					ip += "/" + filepath.ToSlash(relInMod)
				}
				pkg = &Package{Module: mod, ImportPath: ip, Dir: dir}
				pkgByDir[dir] = pkg
				g.Packages = append(g.Packages, pkg)
			}
			pkg.Files = append(pkg.Files, rel)
			pkg.LOC += fset.File(f.Pos()).LineCount()
			if pkg.Doc == "" && f.Doc != nil {
				pkg.Doc = firstSentence(f.Doc.Text())
			}
			collectFile(fset, f, rel, pkg, modPaths, g)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Slice(g.Packages, func(i, j int) bool { return g.Packages[i].Dir < g.Packages[j].Dir })
	resolveCalls(g)
	sort.Slice(g.Endpoints, func(i, j int) bool {
		if g.Endpoints[i].Service != g.Endpoints[j].Service {
			return g.Endpoints[i].Service < g.Endpoints[j].Service
		}
		return g.Endpoints[i].Path < g.Endpoints[j].Path
	})
	return g, nil
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// renderNode pretty-prints an AST node on one line.
func renderNode(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, n)
	s := buf.String()
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func collectFile(fset *token.FileSet, f *ast.File, rel string, pkg *Package, modPaths map[string]string, g *Graph) {
	// import alias -> import path (workspace-internal detection)
	aliases := map[string]string{}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		aliases[name] = p
		for _, mp := range modPaths {
			if p == mp || strings.HasPrefix(p, mp+"/") {
				if !contains(pkg.Imports, p) {
					pkg.Imports = append(pkg.Imports, p)
				}
			}
		}
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sym := &Symbol{
				Name:      d.Name.Name,
				Kind:      "func",
				File:      rel,
				StartLine: fset.Position(d.Pos()).Line,
				EndLine:   fset.Position(d.End()).Line,
				Exported:  d.Name.IsExported(),
			}
			if d.Doc != nil {
				sym.Doc = firstSentence(d.Doc.Text())
			}
			if d.Recv != nil && len(d.Recv.List) > 0 {
				sym.Kind = "method"
				sym.Recv = recvName(d.Recv.List[0].Type)
			}
			body := d.Body
			d2 := *d
			d2.Body = nil
			d2.Doc = nil
			sym.Signature = renderNode(fset, &d2)
			if body != nil {
				sym.Calls = rawCalls(body, aliases)
			}
			pkg.Symbols = append(pkg.Symbols, sym)
			if body != nil {
				collectEndpoints(fset, body, rel, pkg.Module, g)
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				sym := &Symbol{
					Name:      ts.Name.Name,
					File:      rel,
					StartLine: fset.Position(ts.Pos()).Line,
					EndLine:   fset.Position(ts.End()).Line,
					Exported:  ts.Name.IsExported(),
				}
				doc := d.Doc
				if ts.Doc != nil {
					doc = ts.Doc
				}
				if doc != nil {
					sym.Doc = firstSentence(doc.Text())
				}
				switch t := ts.Type.(type) {
				case *ast.StructType:
					sym.Kind = "struct"
					var fields []string
					for _, fl := range t.Fields.List {
						for _, n := range fl.Names {
							fields = append(fields, n.Name)
						}
					}
					sym.Signature = fmt.Sprintf("struct{%s}", strings.Join(fields, ", "))
				case *ast.InterfaceType:
					sym.Kind = "interface"
					var methods []string
					for _, m := range t.Methods.List {
						for _, n := range m.Names {
							methods = append(methods, n.Name)
						}
					}
					sym.Signature = fmt.Sprintf("interface{%s}", strings.Join(methods, ", "))
				default:
					sym.Kind = "type"
					sym.Signature = "type " + ts.Name.Name + " " + renderNode(fset, ts.Type)
				}
				pkg.Symbols = append(pkg.Symbols, sym)
			}
		}
	}
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	}
	return ""
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// rawCalls records call expressions as either "ident" (same-package call),
// "importpath#Sel" (cross-package call via alias), or "?.Sel" (method call on
// a value — resolved later only if the method name is unique in the workspace).
func rawCalls(body *ast.BlockStmt, aliases map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var key string
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			key = fn.Name
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok {
				if imp, isPkg := aliases[x.Name]; isPkg {
					key = imp + "#" + fn.Sel.Name
				} else {
					key = "?." + fn.Sel.Name
				}
			} else {
				key = "?." + fn.Sel.Name
			}
		}
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
		return true
	})
	return out
}

// resolveCalls rewrites raw call keys into workspace symbol IDs and drops
// everything that doesn't resolve (stdlib, third-party, unresolvable methods).
func resolveCalls(g *Graph) {
	// name indexes
	funcsByPkg := map[string]map[string]bool{}      // pkg dir -> plain func names
	pkgByImport := map[string]string{}              // import path -> pkg dir
	methodOwners := map[string][]string{}           // method name -> symbol IDs
	for _, p := range g.Packages {
		pkgByImport[p.ImportPath] = p.Dir
		funcsByPkg[p.Dir] = map[string]bool{}
		for _, s := range p.Symbols {
			if s.Kind == "func" {
				funcsByPkg[p.Dir][s.Name] = true
			}
			if s.Kind == "method" {
				methodOwners[s.Name] = append(methodOwners[s.Name], symID(p.Dir, s.Recv, s.Name))
			}
		}
	}

	for _, p := range g.Packages {
		for _, s := range p.Symbols {
			var resolved []string
			for _, c := range s.Calls {
				switch {
				case strings.HasPrefix(c, "?."):
					name := strings.TrimPrefix(c, "?.")
					if owners := methodOwners[name]; len(owners) == 1 {
						resolved = append(resolved, owners[0])
					}
				case strings.Contains(c, "#"):
					parts := strings.SplitN(c, "#", 2)
					if dir, ok := pkgByImport[parts[0]]; ok {
						if funcsByPkg[dir][parts[1]] {
							resolved = append(resolved, symID(dir, "", parts[1]))
						} else if owners := methodOwners[parts[1]]; len(owners) == 1 {
							resolved = append(resolved, owners[0])
						}
					}
				default:
					if funcsByPkg[p.Dir][c] {
						resolved = append(resolved, symID(p.Dir, "", c))
					}
				}
			}
			s.Calls = resolved
		}
	}
}

// ─── endpoint extraction (Gin) ───────────────────────────────────────────────

var httpMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "HEAD": true, "OPTIONS": true, "Any": true}

// collectEndpoints scans a function body for r.POST("/path", handler)-style
// registrations, composing Group("...") prefixes tracked per local variable.
func collectEndpoints(fset *token.FileSet, body *ast.BlockStmt, rel, service string, g *Graph) {
	prefixes := map[string]string{} // var name -> accumulated path prefix

	ast.Inspect(body, func(n ast.Node) bool {
		// v := x.Group("lit")
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" && len(call.Args) >= 1 {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						base := ""
						if x, ok := sel.X.(*ast.Ident); ok {
							base = prefixes[x.Name]
						}
						if id, ok := as.Lhs[0].(*ast.Ident); ok {
							prefixes[id.Name] = base + strings.Trim(lit.Value, `"`)
						}
					}
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !httpMethods[sel.Sel.Name] || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		prefix := ""
		if x, ok := sel.X.(*ast.Ident); ok {
			prefix = prefixes[x.Name]
		}
		g.Endpoints = append(g.Endpoints, &Endpoint{
			Service: service,
			Method:  strings.ToUpper(sel.Sel.Name),
			Path:    prefix + strings.Trim(lit.Value, `"`),
			Handler: renderNode(fset, call.Args[len(call.Args)-1]),
			File:    rel,
			Line:    fset.Position(call.Pos()).Line,
		})
		return true
	})
}

// ─── queries ─────────────────────────────────────────────────────────────────

func cmdShake(g *Graph) {
	fmt.Println("REPO SHAKE — structural overview (AST-derived; line refs are exact)")
	byModule := map[string][]*Package{}
	var modules []string
	for _, p := range g.Packages {
		if _, ok := byModule[p.Module]; !ok {
			modules = append(modules, p.Module)
		}
		byModule[p.Module] = append(byModule[p.Module], p)
	}
	sort.Strings(modules)
	for _, m := range modules {
		loc, funcs := 0, 0
		for _, p := range byModule[m] {
			loc += p.LOC
			funcs += len(p.Symbols)
		}
		nEp := 0
		for _, e := range g.Endpoints {
			if e.Service == m {
				nEp++
			}
		}
		ep := ""
		if nEp > 0 {
			ep = fmt.Sprintf(", %d endpoints", nEp)
		}
		fmt.Printf("\n%s (%d LOC, %d symbols%s)\n", m, loc, funcs, ep)
		for _, p := range byModule[m] {
			doc := p.Doc
			if doc == "" {
				doc = summarizePkg(p)
			}
			fmt.Printf("  %-52s %s\n", p.Dir, doc)
		}
	}
	fmt.Printf("\n%d endpoints total — run `endpoints` for the table.\n", len(g.Endpoints))
	fmt.Println("Next: `package <dir>` | `symbol <name>` | `callers <name>` | `file <path>`")
}

// summarizePkg synthesizes a one-liner when a package has no doc comment.
func summarizePkg(p *Package) string {
	var exported []string
	for _, s := range p.Symbols {
		if s.Exported && (s.Kind == "func" || s.Kind == "interface") {
			exported = append(exported, s.Name)
		}
		if len(exported) == 4 {
			exported = append(exported, "…")
			break
		}
	}
	return strings.Join(exported, ", ")
}

func cmdEndpoints(g *Graph) {
	for _, e := range g.Endpoints {
		fmt.Printf("%-18s %-7s %-42s → %-28s %s:%d\n", e.Service, e.Method, e.Path, e.Handler, e.File, e.Line)
	}
}

func findPackage(g *Graph, q string) *Package {
	for _, p := range g.Packages {
		if p.Dir == q || p.ImportPath == q {
			return p
		}
	}
	for _, p := range g.Packages {
		if strings.HasSuffix(p.Dir, q) || strings.Contains(p.Dir, q) {
			return p
		}
	}
	return nil
}

func cmdPackage(g *Graph, q string) {
	p := findPackage(g, q)
	if p == nil {
		fmt.Printf("no package matching %q — run `shake` to list packages\n", q)
		return
	}
	fmt.Printf("PACKAGE %s (%s, %d LOC)\n", p.Dir, p.ImportPath, p.LOC)
	if p.Doc != "" {
		fmt.Println(p.Doc)
	}
	if len(p.Imports) > 0 {
		fmt.Printf("workspace imports: %s\n", strings.Join(p.Imports, ", "))
	}
	for _, kind := range []string{"interface", "struct", "type", "func", "method"} {
		for _, s := range p.Symbols {
			if s.Kind != kind {
				continue
			}
			fmt.Printf("  %-9s %-30s %s:%d-%d\n", s.Kind, displayName(s), s.File, s.StartLine, s.EndLine)
			fmt.Printf("            %s\n", s.Signature)
		}
	}
}

func displayName(s *Symbol) string {
	if s.Recv != "" {
		return s.Recv + "." + s.Name
	}
	return s.Name
}

func matchSymbols(g *Graph, q string) []*struct {
	P *Package
	S *Symbol
} {
	var out []*struct {
		P *Package
		S *Symbol
	}
	lq := strings.ToLower(q)
	exact := func(s *Symbol) bool { return s.Name == q || displayName(s) == q }
	fuzzy := func(s *Symbol) bool { return strings.Contains(strings.ToLower(displayName(s)), lq) }
	for pass, match := range []func(*Symbol) bool{exact, fuzzy} {
		for _, p := range g.Packages {
			for _, s := range p.Symbols {
				if match(s) {
					out = append(out, &struct {
						P *Package
						S *Symbol
					}{p, s})
				}
			}
		}
		if pass == 0 && len(out) > 0 {
			return out
		}
	}
	return out
}

func cmdSymbol(g *Graph, q string) {
	matches := matchSymbols(g, q)
	if len(matches) == 0 {
		fmt.Printf("no symbol matching %q\n", q)
		return
	}
	for _, m := range matches {
		s := m.S
		fmt.Printf("%s %s — %s:%d-%d\n", s.Kind, displayName(s), s.File, s.StartLine, s.EndLine)
		fmt.Printf("  %s\n", s.Signature)
		if s.Doc != "" {
			fmt.Printf("  doc: %s\n", s.Doc)
		}
		if len(s.Calls) > 0 {
			fmt.Printf("  calls: %s\n", strings.Join(s.Calls, ", "))
		}
		callers := callersOf(g, symID(m.P.Dir, s.Recv, s.Name))
		if len(callers) > 0 {
			fmt.Printf("  called by: %s\n", strings.Join(callers, ", "))
		}
		fmt.Println()
	}
}

func callersOf(g *Graph, id string) []string {
	var out []string
	for _, p := range g.Packages {
		for _, s := range p.Symbols {
			for _, c := range s.Calls {
				if c == id {
					out = append(out, fmt.Sprintf("%s (%s:%d)", displayName(s), s.File, s.StartLine))
				}
			}
		}
	}
	return out
}

func cmdCallers(g *Graph, q string) {
	matches := matchSymbols(g, q)
	if len(matches) == 0 {
		fmt.Printf("no symbol matching %q\n", q)
		return
	}
	for _, m := range matches {
		id := symID(m.P.Dir, m.S.Recv, m.S.Name)
		callers := callersOf(g, id)
		fmt.Printf("%s — %d caller(s)\n", displayName(m.S), len(callers))
		for _, c := range callers {
			fmt.Printf("  %s\n", c)
		}
	}
}

func cmdFile(g *Graph, q string) {
	found := false
	for _, p := range g.Packages {
		for _, s := range p.Symbols {
			if s.File == q || strings.HasSuffix(s.File, q) {
				if !found {
					fmt.Printf("FILE %s\n", s.File)
					found = true
				}
				fmt.Printf("  %-9s %-30s lines %d-%d\n", s.Kind, displayName(s), s.StartLine, s.EndLine)
			}
		}
	}
	if !found {
		fmt.Printf("no file matching %q\n", q)
	}
}

// ─── main ────────────────────────────────────────────────────────────────────

func usage() {
	fmt.Println(`repograph — structural code graph for token-efficient repo navigation

usage: go run ./tools/repograph <command> [arg]

  shake             high-level repo overview (start here)
  endpoints         all HTTP endpoints: method, path, handler, file:line
  package <dir>     one package: symbols, signatures, line ranges
  symbol <name>     find symbol(s): signature, doc, calls, callers, location
  callers <name>    who calls a function/method
  file <path>       symbols in one file with line ranges
  json              dump the full graph as JSON`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Allow running from anywhere inside the repo: walk up to go.work.
	for r := root; ; r = filepath.Dir(r) {
		if _, err := os.Stat(filepath.Join(r, "go.work")); err == nil {
			root = r
			break
		}
		if r == filepath.Dir(r) {
			fmt.Fprintln(os.Stderr, "go.work not found — run inside the repo")
			os.Exit(1)
		}
	}

	g, err := buildGraph(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cmd := os.Args[1]
	arg := ""
	if len(os.Args) > 2 {
		arg = os.Args[2]
	}
	switch cmd {
	case "shake":
		cmdShake(g)
	case "endpoints":
		cmdEndpoints(g)
	case "package":
		cmdPackage(g, arg)
	case "symbol":
		cmdSymbol(g, arg)
	case "callers":
		cmdCallers(g, arg)
	case "file":
		cmdFile(g, arg)
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(g)
	default:
		usage()
		os.Exit(1)
	}
}
