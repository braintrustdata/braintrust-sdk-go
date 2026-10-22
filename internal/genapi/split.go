package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	clientFile = "client.gen.go"
	modelsFile = "models.gen.go"
)

var generatedMarker = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// operationTags maps each generated operation's Go name to its single spec tag.
func operationTags(spec []byte) (map[string]string, error) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("decode specification paths: %w", err)
	}
	tags := map[string]string{}
	for path, item := range doc.Paths {
		for method, raw := range item {
			var operation struct {
				OperationID string   `json:"operationId"`
				Tags        []string `json:"tags"`
			}
			// Path items also hold non-operation fields such as shared parameters.
			if err := json.Unmarshal(raw, &operation); err != nil || operation.OperationID == "" {
				continue
			}
			if len(operation.Tags) != 1 {
				return nil, fmt.Errorf("%s %s has %d tags; the generated client is split by exactly one tag", method, path, len(operation.Tags))
			}
			name := []rune(operation.OperationID)
			name[0] = unicode.ToUpper(name[0])
			tags[string(name)] = operation.Tags[0]
		}
	}
	return tags, nil
}

// splitGenerated distributes oapi-codegen's single file into one file per tag.
// Operation code goes to its tag's file, as do models reachable only from that
// tag's operations. Shared models and client plumbing get their own files.
// opTags may include tags that were not generated; they are ignored.
func splitGenerated(source []byte, opTags map[string]string) (map[string][]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse generated client: %w", err)
	}
	offset := func(pos token.Pos) int { return fset.Position(pos).Offset }

	packageDoc := string(source[:offset(file.Package)])
	marker := generatedMarker.FindString(packageDoc)
	if marker == "" {
		return nil, fmt.Errorf("generated client lacks a generated-code marker")
	}

	types := map[string]bool{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.TYPE {
			for _, spec := range gen.Specs {
				types[spec.(*ast.TypeSpec).Name.Name] = true //nolint:forcetypeassert // type declarations only hold type specs
			}
		}
	}

	var units []*unit
	var imports []*ast.ImportSpec
	covered := []struct{ start, end int }{{0, offset(file.Name.End())}}
	generatedOps := map[string]bool{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			for _, spec := range gen.Specs {
				imports = append(imports, spec.(*ast.ImportSpec)) //nolint:forcetypeassert // import declarations only hold import specs
			}
			covered = append(covered, struct{ start, end int }{offset(gen.Pos()), offset(gen.End())})
			continue
		}
		start := decl.Pos()
		var doc *ast.CommentGroup
		switch d := decl.(type) {
		case *ast.FuncDecl:
			doc = d.Doc
		case *ast.GenDecl:
			doc = d.Doc
		}
		if doc != nil {
			start = doc.Pos()
		}
		u := &unit{text: string(source[offset(start):offset(decl.End())]), refs: map[string]bool{}, imports: map[string]bool{}}
		covered = append(covered, struct{ start, end int }{offset(start), offset(decl.End())})

		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil {
				receiver := receiverName(d.Recv.List[0].Type)
				if receiver == "Client" || receiver == "ClientWithResponses" {
					u.op = matchOperation(name, opTags, "", "WithBodyWithResponse", "WithResponse", "WithBody", "")
					if u.op != "" {
						generatedOps[u.op] = true
					}
				} else {
					u.owner = receiver
				}
			} else {
				u.op = matchOperation(name, opTags, "New", "RequestWithBody", "Request")
				if u.op == "" {
					u.op = matchOperation(name, opTags, "Parse", "Response")
				}
			}
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				u.owner = d.Specs[0].(*ast.TypeSpec).Name.Name //nolint:forcetypeassert // type declarations only hold type specs
			case token.CONST:
				// oapi-codegen groups enum values per type.
				if spec, ok := d.Specs[0].(*ast.ValueSpec); ok {
					if ident, ok := spec.Type.(*ast.Ident); ok && types[ident.Name] {
						u.owner = ident.Name
					}
				}
			}
		}

		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if ident, ok := n.X.(*ast.Ident); ok {
					u.imports[ident.Name] = true
				}
				ast.Inspect(n.X, func(n ast.Node) bool {
					if ident, ok := n.(*ast.Ident); ok && types[ident.Name] {
						u.refs[ident.Name] = true
					}
					return true
				})
				return false
			case *ast.Ident:
				if types[n.Name] {
					u.refs[n.Name] = true
				}
			}
			return true
		})
		units = append(units, u)
	}

	for _, group := range file.Comments {
		start, end := offset(group.Pos()), offset(group.End())
		inside := false
		for _, span := range covered {
			inside = inside || (start >= span.start && end <= span.end)
		}
		if !inside {
			return nil, fmt.Errorf("comment at %s is not attached to a declaration", fset.Position(group.Pos()))
		}
	}
	// Tags excluded from generation have no methods; any other operation must.
	generatedTags := map[string]bool{}
	for op := range generatedOps {
		generatedTags[opTags[op]] = true
	}
	for op, tag := range opTags {
		if generatedTags[tag] && !generatedOps[op] {
			return nil, fmt.Errorf("operation %s has no generated client method", op)
		}
	}

	// Each type's references include those of its methods and enum values.
	typeRefs := map[string]map[string]bool{}
	for _, u := range units {
		if u.owner == "" {
			continue
		}
		if typeRefs[u.owner] == nil {
			typeRefs[u.owner] = map[string]bool{}
		}
		for ref := range u.refs {
			typeRefs[u.owner][ref] = true
		}
	}
	typeTags := map[string]map[string]bool{}
	var visit func(name, tag string)
	visit = func(name, tag string) {
		if typeTags[name] == nil {
			typeTags[name] = map[string]bool{}
		}
		if typeTags[name][tag] {
			return
		}
		typeTags[name][tag] = true
		for ref := range typeRefs[name] {
			visit(ref, tag)
		}
	}
	for _, u := range units {
		if u.op != "" {
			for ref := range u.refs {
				visit(ref, opTags[u.op])
			}
		}
	}

	fileOf := func(u *unit) string {
		if u.op != "" {
			return tagFileName(opTags[u.op])
		}
		if u.owner == "" || u.owner == "Client" || u.owner == "ClientWithResponses" || strings.HasPrefix(u.owner, "Client") && strings.HasSuffix(u.owner, "Interface") {
			return clientFile
		}
		if tags := typeTags[u.owner]; len(tags) == 1 {
			for tag := range tags {
				return tagFileName(tag)
			}
		}
		return modelsFile
	}
	bodies := map[string][]string{}
	used := map[string]map[string]bool{}
	for _, u := range units {
		name := fileOf(u)
		bodies[name] = append(bodies[name], u.text)
		if used[name] == nil {
			used[name] = map[string]bool{}
		}
		for imp := range u.imports {
			used[name][imp] = true
		}
	}

	files := map[string][]byte{}
	for name, decls := range bodies {
		var out bytes.Buffer
		if name == clientFile {
			out.WriteString(packageDoc)
		} else {
			out.WriteString(marker + "\n\n")
		}
		out.WriteString("package " + file.Name.Name + "\n\n")
		var kept []string
		for _, spec := range imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, err
			}
			local := path[strings.LastIndex(path, "/")+1:]
			text := spec.Path.Value
			if spec.Name != nil {
				local = spec.Name.Name
				text = local + " " + text
			}
			if used[name][local] {
				kept = append(kept, text)
			}
		}
		if len(kept) > 0 {
			out.WriteString("import (\n\t" + strings.Join(kept, "\n\t") + "\n)\n\n")
		}
		out.WriteString(strings.Join(decls, "\n\n") + "\n")
		formatted, err := format.Source(out.Bytes())
		if err != nil {
			return nil, fmt.Errorf("format %s: %w", name, err)
		}
		files[name] = formatted
	}
	return files, nil
}

// unit is one top-level declaration of the generated file.
type unit struct {
	text    string
	owner   string // type whose file this declaration follows
	op      string // operation whose file this declaration belongs to
	refs    map[string]bool
	imports map[string]bool
}

func receiverName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// matchOperation strips prefix and the first matching suffix from name and
// returns the operation it names, if any.
func matchOperation(name string, opTags map[string]string, prefix string, suffixes ...string) string {
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	name = strings.TrimPrefix(name, prefix)
	for _, suffix := range suffixes {
		if op := strings.TrimSuffix(name, suffix); strings.HasSuffix(name, suffix) {
			if _, ok := opTags[op]; ok {
				return op
			}
		}
	}
	return ""
}

// tagFileName converts a spec tag such as AiSecrets to ai_secrets.gen.go.
func tagFileName(tag string) string {
	var out []rune
	runes := []rune(tag)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
			out = append(out, '_')
		}
		out = append(out, unicode.ToLower(r))
	}
	return string(out) + ".gen.go"
}
