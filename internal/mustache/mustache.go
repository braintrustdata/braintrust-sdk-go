// Package mustache is a vendored copy of github.com/cbroglie/mustache.
//
// See VENDOR.md for the upstream revision and the list of local modifications.
// Nothing outside this directory should modify these files casually: keeping
// them close to upstream is what makes re-syncing a readable diff.
package mustache

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"reflect"
	"strconv"
	"strings"
)

var (
	// AllowMissingVariables defines the behavior for a variable "miss." If it
	// is true (the default), an empty string is emitted. If it is false, an error
	// is generated instead.
	AllowMissingVariables = true
)

// RenderFunc is provided to lambda functions for rendering.
type RenderFunc func(text string) (string, error)

// LambdaFunc is the signature for lambda functions.
type LambdaFunc func(text string, render RenderFunc) (string, error)

// EscapeFunc is used for escaping non-raw values in templates.
type EscapeFunc func(text string) string

// ValueFunc converts an interpolated value to its string form. It is a
// braintrust addition (see VENDOR.md): upstream always uses fmt.Sprint, which
// renders maps and slices in Go syntax rather than JSON.
type ValueFunc func(value interface{}) string

// A TagType represents the specific type of mustache tag that a Tag
// represents. The zero TagType is not a valid type.
type TagType uint

// Defines representing the possible Tag types
const (
	Invalid TagType = iota
	Variable
	Section
	InvertedSection
	Partial
)

// Skip all whitespaces apeared after these types of tags until end of line
// if the line only contains a tag and whitespaces.
const (
	SkipWhitespaceTagTypes = "#^/<>=!"
)

func (t TagType) String() string {
	if int(t) < len(tagNames) {
		return tagNames[t]
	}
	return "type" + strconv.Itoa(int(t))
}

var tagNames = []string{
	Invalid:         "Invalid",
	Variable:        "Variable",
	Section:         "Section",
	InvertedSection: "InvertedSection",
	Partial:         "Partial",
}

// Tag represents the different mustache tag types.
//
// Not all methods apply to all kinds of tags. Restrictions, if any, are noted
// in the documentation for each method. Use the Type method to find out the
// type of tag before calling type-specific methods. Calling a method
// inappropriate to the type of tag causes a run time panic.
type Tag interface {
	// Type returns the type of the tag.
	Type() TagType
	// Name returns the name of the tag.
	Name() string
	// Tags returns any child tags. It panics for tag types which cannot contain
	// child tags (i.e. variable tags).
	Tags() []Tag
}

type textElement struct {
	text []byte
}

type varElement struct {
	name string
	raw  bool
}

type sectionElement struct {
	name      string
	inverted  bool
	startline int
	elems     []interface{}
}

type partialElement struct {
	name   string
	indent string
	prov   PartialProvider
}

// Template represents a compilde mustache template
type Template struct {
	data     string
	otag     string
	ctag     string
	p        int
	curline  int
	elems    []interface{}
	forceRaw bool
	partial  PartialProvider
	escape   EscapeFunc
	value    ValueFunc
}

// Tags returns the mustache tags for the given template
func (tmpl *Template) Tags() []Tag {
	return extractTags(tmpl.elems)
}

// Escape sets custom escape function. By-default it is HTMLEscape.
func (tmpl *Template) Escape(fn EscapeFunc) {
	tmpl.escape = fn
}

// Value sets a custom value-to-string function, used for both escaped
// ({{var}}) and raw ({{{var}}}) interpolation. By default it is fmt.Sprint.
// Braintrust addition; see VENDOR.md.
func (tmpl *Template) Value(fn ValueFunc) {
	tmpl.value = fn
}

// renderValue converts an interpolated value to a string, using the custom
// ValueFunc when one is set. Braintrust addition; see VENDOR.md.
func (tmpl *Template) renderValue(value interface{}) string {
	if tmpl.value != nil {
		return tmpl.value(value)
	}
	return fmt.Sprint(value)
}

func extractTags(elems []interface{}) []Tag {
	tags := make([]Tag, 0, len(elems))
	for _, elem := range elems {
		switch elem := elem.(type) {
		case *varElement:
			tags = append(tags, elem)
		case *sectionElement:
			tags = append(tags, elem)
		case *partialElement:
			tags = append(tags, elem)
		}
	}
	return tags
}

func (e *varElement) Type() TagType {
	return Variable
}

func (e *varElement) Name() string {
	return e.name
}

func (e *varElement) Tags() []Tag {
	panic("mustache: Tags on Variable type")
}

func (e *sectionElement) Type() TagType {
	if e.inverted {
		return InvertedSection
	}
	return Section
}

func (e *sectionElement) Name() string {
	return e.name
}

func (e *sectionElement) Tags() []Tag {
	return extractTags(e.elems)
}

func (e *partialElement) Type() TagType {
	return Partial
}

func (e *partialElement) Name() string {
	return e.name
}

func (e *partialElement) Tags() []Tag {
	return nil
}

func (tmpl *Template) readString(s string) (string, error) {
	newlines := 0
	for i := tmpl.p; ; i++ {
		//are we at the end of the string?
		if i+len(s) > len(tmpl.data) {
			return tmpl.data[tmpl.p:], io.EOF
		}

		if tmpl.data[i] == '\n' {
			newlines++
		}

		if tmpl.data[i] != s[0] {
			continue
		}

		match := true
		for j := 1; j < len(s); j++ {
			if s[j] != tmpl.data[i+j] {
				match = false
				break
			}
		}

		if match {
			e := i + len(s)
			text := tmpl.data[tmpl.p:e]
			tmpl.p = e

			tmpl.curline += newlines
			return text, nil
		}
	}
}

type textReadingResult struct {
	text          string
	padding       string
	mayStandalone bool
}

func (tmpl *Template) readText() (*textReadingResult, error) {
	pPrev := tmpl.p
	text, err := tmpl.readString(tmpl.otag)
	if err == io.EOF {
		return &textReadingResult{
			text:          text,
			padding:       "",
			mayStandalone: false,
		}, err
	}

	var i int
	for i = tmpl.p - len(tmpl.otag); i > pPrev; i-- {
		if tmpl.data[i-1] != ' ' && tmpl.data[i-1] != '\t' {
			break
		}
	}

	mayStandalone := (i == 0 || tmpl.data[i-1] == '\n')

	if mayStandalone {
		return &textReadingResult{
			text:          tmpl.data[pPrev:i],
			padding:       tmpl.data[i : tmpl.p-len(tmpl.otag)],
			mayStandalone: true,
		}, nil
	}

	return &textReadingResult{
		text:          tmpl.data[pPrev : tmpl.p-len(tmpl.otag)],
		padding:       "",
		mayStandalone: false,
	}, nil
}

type tagReadingResult struct {
	tag        string
	standalone bool
}

func (tmpl *Template) readTag(mayStandalone bool) (*tagReadingResult, error) {
	var text string
	var err error
	if tmpl.p < len(tmpl.data) && tmpl.data[tmpl.p] == '{' {
		text, err = tmpl.readString("}" + tmpl.ctag)
	} else {
		text, err = tmpl.readString(tmpl.ctag)
	}

	if err == io.EOF {
		//put the remaining text in a block
		return nil, newError(tmpl.curline, ErrUnmatchedOpenTag)
	}

	text = text[:len(text)-len(tmpl.ctag)]

	//trim the close tag off the text
	tag := strings.TrimSpace(text)
	if len(tag) == 0 {
		return nil, newError(tmpl.curline, ErrEmptyTag)
	}

	eow := tmpl.p
	for i := tmpl.p; i < len(tmpl.data); i++ {
		if !(tmpl.data[i] == ' ' || tmpl.data[i] == '\t') {
			eow = i
			break
		}
	}

	standalone := true
	if mayStandalone {
		if !strings.Contains(SkipWhitespaceTagTypes, tag[0:1]) {
			standalone = false
		} else {
			if eow == len(tmpl.data) {
				standalone = true
				tmpl.p = eow
			} else if eow < len(tmpl.data) && tmpl.data[eow] == '\n' {
				standalone = true
				tmpl.p = eow + 1
				tmpl.curline++
			} else if eow+1 < len(tmpl.data) && tmpl.data[eow] == '\r' && tmpl.data[eow+1] == '\n' {
				standalone = true
				tmpl.p = eow + 2
				tmpl.curline++
			} else {
				standalone = false
			}
		}
	}

	return &tagReadingResult{
		tag:        tag,
		standalone: standalone,
	}, nil
}

// parsePartial rejects partial tags.
//
// Braintrust modification (see VENDOR.md): a partial pulls in content from
// outside the template, which is exactly what must not happen when the template
// itself is untrusted. Upstream resolves partials through a PartialProvider --
// by default a filesystem one. Failing at parse time means a prompt that uses a
// partial is reported instead of silently rendering as nothing.
func (tmpl *Template) parsePartial(name, indent string) (*partialElement, error) {
	return nil, newErrorWithReason(tmpl.curline, ErrPartialNotSupported, name)
}

func (tmpl *Template) parseSection(section *sectionElement) error {
	for {
		textResult, err := tmpl.readText()
		text := textResult.text
		padding := textResult.padding
		mayStandalone := textResult.mayStandalone

		if err == io.EOF {
			//put the remaining text in a block
			return newErrorWithReason(section.startline, ErrSectionNoClosingTag, section.name)
		}

		// put text into an item
		section.elems = append(section.elems, &textElement{[]byte(text)})

		tagResult, err := tmpl.readTag(mayStandalone)
		if err != nil {
			return err
		}

		if !tagResult.standalone {
			section.elems = append(section.elems, &textElement{[]byte(padding)})
		}

		tag := tagResult.tag
		switch tag[0] {
		case '!':
			//ignore comment
		case '#', '^':
			name := strings.TrimSpace(tag[1:])
			se := sectionElement{name, tag[0] == '^', tmpl.curline, []interface{}{}}
			err := tmpl.parseSection(&se)
			if err != nil {
				return err
			}
			section.elems = append(section.elems, &se)
		case '/':
			name := strings.TrimSpace(tag[1:])
			if name != section.name {
				return newErrorWithReason(tmpl.curline, ErrInterleavedClosingTag, name)
			}
			return nil
		case '>':
			name := strings.TrimSpace(tag[1:])
			partial, err := tmpl.parsePartial(name, textResult.padding)
			if err != nil {
				return err
			}
			section.elems = append(section.elems, partial)
		case '=':
			tmpl.parseCustomDelimiter(tag)
		case '{':
			if tag[len(tag)-1] == '}' {
				//use a raw tag
				name := strings.TrimSpace(tag[1 : len(tag)-1])
				section.elems = append(section.elems, &varElement{name, true})
			}
		case '&':
			name := strings.TrimSpace(tag[1:])
			section.elems = append(section.elems, &varElement{name, true})
		default:
			section.elems = append(section.elems, &varElement{tag, tmpl.forceRaw})
		}
	}
}

func (tmpl *Template) parse() error {
	for {
		textResult, err := tmpl.readText()
		text := textResult.text
		padding := textResult.padding
		mayStandalone := textResult.mayStandalone

		if err == io.EOF {
			//put the remaining text in a block
			tmpl.elems = append(tmpl.elems, &textElement{[]byte(text)})
			return nil
		}

		// put text into an item
		tmpl.elems = append(tmpl.elems, &textElement{[]byte(text)})

		tagResult, err := tmpl.readTag(mayStandalone)
		if err != nil {
			return err
		}

		if !tagResult.standalone {
			tmpl.elems = append(tmpl.elems, &textElement{[]byte(padding)})
		}

		tag := tagResult.tag
		switch tag[0] {
		case '!':
			//ignore comment
		case '#', '^':
			name := strings.TrimSpace(tag[1:])
			se := sectionElement{name, tag[0] == '^', tmpl.curline, []interface{}{}}
			err := tmpl.parseSection(&se)
			if err != nil {
				return err
			}
			tmpl.elems = append(tmpl.elems, &se)
		case '/':
			return newError(tmpl.curline, ErrUnmatchedCloseTag)
		case '>':
			name := strings.TrimSpace(tag[1:])
			partial, err := tmpl.parsePartial(name, textResult.padding)
			if err != nil {
				return err
			}
			tmpl.elems = append(tmpl.elems, partial)
		case '=':
			tmpl.parseCustomDelimiter(tag)
		case '{':
			//use a raw tag
			if tag[len(tag)-1] == '}' {
				name := strings.TrimSpace(tag[1 : len(tag)-1])
				tmpl.elems = append(tmpl.elems, &varElement{name, true})
			}
		case '&':
			name := strings.TrimSpace(tag[1:])
			tmpl.elems = append(tmpl.elems, &varElement{name, true})
		default:
			tmpl.elems = append(tmpl.elems, &varElement{tag, tmpl.forceRaw})
		}
	}
}

func (tmpl *Template) parseCustomDelimiter(tag string) {
	if len(tag) < 3 || tag[len(tag)-1] != '=' {
		tmpl.elems = append(tmpl.elems, &varElement{tag, tmpl.forceRaw})
		return
	}
	trimmedtag := strings.TrimSpace(tag[1 : len(tag)-1])
	newtags := strings.SplitN(trimmedtag, " ", 2)
	if len(newtags) == 2 {
		tmpl.otag = newtags[0]
		tmpl.ctag = newtags[1]
	} else {
		tmpl.elems = append(tmpl.elems, &varElement{tag, tmpl.forceRaw})
	}
}

// Evaluate interfaces and pointers looking for a value that can look up the name, via a
// struct field, method, or map key, and return the result of the lookup.
func lookup(contextChain []interface{}, name string, allowMissing bool) (reflect.Value, error) {
	// dot notation
	if name != "." && strings.Contains(name, ".") {
		parts := strings.SplitN(name, ".", 2)

		v, err := lookup(contextChain, parts[0], allowMissing)
		if err != nil {
			return v, err
		}
		return lookup([]interface{}{v}, parts[1], allowMissing)
	}

	// Braintrust modification (see VENDOR.md): upstream printed panics to
	// stdout. Under `bt eval` stdout carries the eval manifest, so anything
	// written there corrupts the protocol. Recover without reporting; the
	// lookup simply yields no value.
	defer func() {
		_ = recover()
	}()

Outer:
	for _, ctx := range contextChain {
		v := ctx.(reflect.Value)
		for v.IsValid() {
			// Braintrust modification (see VENDOR.md): upstream resolves a name
			// against zero-argument METHODS before fields, so template text --
			// which arrives from Braintrust -- could choose a Go method to
			// invoke on caller data. Templates interpolate values; they never
			// call code.
			if name == "." {
				return v, nil
			}
			switch av := v; av.Kind() {
			case reflect.Ptr:
				v = av.Elem()
			case reflect.Interface:
				v = av.Elem()
			case reflect.Struct:
				ret := av.FieldByName(name)
				if ret.IsValid() {
					return ret, nil
				}
				continue Outer
			case reflect.Map:
				ret := av.MapIndex(reflect.ValueOf(name))
				if ret.IsValid() {
					return ret, nil
				}
				continue Outer
			default:
				continue Outer
			}
		}
	}
	if allowMissing {
		return reflect.Value{}, nil
	}
	return reflect.Value{}, fmt.Errorf("missing variable %q", name)
}

func isEmpty(v reflect.Value) bool {
	if !v.IsValid() || v.Interface() == nil {
		return true
	}

	valueInd := indirect(v)
	if !valueInd.IsValid() {
		return true
	}
	switch val := valueInd; val.Kind() {
	case reflect.Array, reflect.Slice:
		return val.Len() == 0
	case reflect.String:
		return len(strings.TrimSpace(val.String())) == 0
	default:
		return valueInd.IsZero()
	}
}

func indirect(v reflect.Value) reflect.Value {
loop:
	for v.IsValid() {
		switch av := v; av.Kind() {
		case reflect.Ptr:
			v = av.Elem()
		case reflect.Interface:
			v = av.Elem()
		default:
			break loop
		}
	}
	return v
}

func (tmpl *Template) renderSection(section *sectionElement, contextChain []interface{}, buf io.Writer) error {
	value, err := lookup(contextChain, section.name, true)
	if err != nil {
		return err
	}
	var context = contextChain[0].(reflect.Value)
	var contexts = []interface{}{}
	// if the value is nil, check if it's an inverted section
	isEmpty := isEmpty(value)
	if isEmpty && !section.inverted || !isEmpty && section.inverted {
		return nil
	} else if !section.inverted {
		valueInd := indirect(value)
		switch val := valueInd; val.Kind() {
		case reflect.Slice:
			for i := 0; i < val.Len(); i++ {
				contexts = append(contexts, val.Index(i))
			}
		case reflect.Array:
			for i := 0; i < val.Len(); i++ {
				contexts = append(contexts, val.Index(i))
			}
		case reflect.Map, reflect.Struct:
			contexts = append(contexts, value)
		case reflect.Func:
			// Braintrust modification (see VENDOR.md): upstream calls a func in
			// the context as a mustache lambda, passing it the section body and
			// re-parsing whatever it returns as a template. That is
			// template-directed code execution plus a re-entrancy path, so a
			// func is simply not a truthy section value here.
			return nil
		default:
			// Spec: Non-false sections have their value at the top of context,
			// accessible as {{.}} or through the parent context. This gives
			// a simple way to display content conditionally if a variable exists.
			contexts = append(contexts, value)
		}
	} else if section.inverted {
		contexts = append(contexts, context)
	}

	chain2 := make([]interface{}, len(contextChain)+1)
	copy(chain2[1:], contextChain)
	//by default we execute the section
	for _, ctx := range contexts {
		chain2[0] = ctx
		for _, elem := range section.elems {
			if err := tmpl.renderElement(elem, chain2, buf); err != nil {
				return err
			}
		}
	}
	return nil
}

func getSectionText(elements []interface{}, buf io.Writer) error {
	for _, element := range elements {
		if err := getElementText(element, buf); err != nil {
			return err
		}
	}
	return nil
}

func getElementText(element interface{}, buf io.Writer) error {
	switch elem := element.(type) {
	case *textElement:
		fmt.Fprintf(buf, "%s", elem.text)
	case *varElement:
		if elem.raw {
			fmt.Fprintf(buf, "{{{%s}}}", elem.name)
		} else {
			fmt.Fprintf(buf, "{{%s}}", elem.name)
		}
	case *sectionElement:
		if elem.inverted {
			fmt.Fprintf(buf, "{{^%s}}", elem.name)
		} else {
			fmt.Fprintf(buf, "{{#%s}}", elem.name)
		}
		for _, nelem := range elem.elems {
			if err := getElementText(nelem, buf); err != nil {
				return err
			}
		}
		fmt.Fprintf(buf, "{{/%s}}", elem.name)
	default:
		return fmt.Errorf("unexpected element type %T", elem)
	}
	return nil
}

func (tmpl *Template) renderElement(element interface{}, contextChain []interface{}, buf io.Writer) error {
	switch elem := element.(type) {
	case *textElement:
		_, err := buf.Write(elem.text)
		return err
	case *varElement:
		// Braintrust modification (see VENDOR.md): see lookup -- nothing may be
		// written to stdout.
		defer func() {
			_ = recover()
		}()
		val, err := lookup(contextChain, elem.name, AllowMissingVariables)
		if err != nil {
			return err
		}

		if val.IsValid() {
			// Braintrust modification (see VENDOR.md): both raw and escaped
			// interpolation go through renderValue, so a custom ValueFunc can
			// JSON-encode non-string values instead of using Go syntax.
			s := tmpl.renderValue(val.Interface())
			if elem.raw {
				_, _ = buf.Write([]byte(s))
			} else {
				_, _ = buf.Write([]byte(tmpl.escape(s)))
			}
		}
	case *sectionElement:
		if err := tmpl.renderSection(elem, contextChain, buf); err != nil {
			return err
		}
	case *partialElement:
		partial, err := getPartials(elem.prov, elem.name, elem.indent)
		if err != nil {
			return err
		}
		if err := partial.renderTemplate(contextChain, buf); err != nil {
			return err
		}
	}
	return nil
}

func (tmpl *Template) renderTemplate(contextChain []interface{}, buf io.Writer) error {
	for _, elem := range tmpl.elems {
		if err := tmpl.renderElement(elem, contextChain, buf); err != nil {
			return err
		}
	}
	return nil
}

// FRender uses the given data source - generally a map or struct - to
// render the compiled template to an io.Writer.
func (tmpl *Template) FRender(out io.Writer, context ...interface{}) error {
	var contextChain []interface{}
	for _, c := range context {
		val := reflect.ValueOf(c)
		contextChain = append(contextChain, val)
	}
	return tmpl.renderTemplate(contextChain, out)
}

// Render uses the given data source - generally a map or struct - to render
// the compiled template and return the output.
func (tmpl *Template) Render(context ...interface{}) (string, error) {
	var buf bytes.Buffer
	err := tmpl.FRender(&buf, context...)
	return buf.String(), err
}

// RenderInLayout uses the given data source - generally a map or struct - to
// render the compiled template and layout "wrapper" template and return the
// output.
func (tmpl *Template) RenderInLayout(layout *Template, context ...interface{}) (string, error) {
	var buf bytes.Buffer
	err := tmpl.FRenderInLayout(&buf, layout, context...)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// FRenderInLayout uses the given data source - generally a map or
// struct - to render the compiled templated a loayout "wrapper"
// template to an io.Writer.
func (tmpl *Template) FRenderInLayout(out io.Writer, layout *Template, context ...interface{}) error {
	content, err := tmpl.Render(context...)
	if err != nil {
		return err
	}
	allContext := make([]interface{}, len(context)+1)
	copy(allContext[1:], context)
	allContext[0] = map[string]string{"content": content}
	return layout.FRender(out, allContext...)
}

// ParseString compiles a mustache template string. The resulting output can
// be used to efficiently render the template multiple times with different data
// sources.
func ParseString(data string) (*Template, error) {
	return ParseStringRaw(data, false)
}

// ParseStringRaw compiles a mustache template string. The resulting output can
// be used to efficiently render the template multiple times with different data
// sources.
func ParseStringRaw(data string, forceRaw bool) (*Template, error) {
	// Braintrust modification (see VENDOR.md): upstream defaults to a
	// FileProvider rooted at $CWD, which makes "{{> /etc/passwd }}" in a
	// template read that file off disk. Templates here come from the Braintrust
	// API, so partials must never touch the filesystem. An empty
	// StaticProvider renders every partial as "".
	return ParseStringPartialsRaw(data, &StaticProvider{}, forceRaw)
}

// ParseStringPartials compiles a mustache template string, retrieving any
// required partials from the given provider. The resulting output can be used
// to efficiently render the template multiple times with different data
// sources.
func ParseStringPartials(data string, partials PartialProvider) (*Template, error) {
	return ParseStringPartialsRaw(data, partials, false)
}

// ParseStringPartialsRaw compiles a mustache template string, retrieving any
// required partials from the given provider. The resulting output can be used
// to efficiently render the template multiple times with different data
// sources.
func ParseStringPartialsRaw(data string, partials PartialProvider, forceRaw bool) (*Template, error) {
	tmpl := Template{data, "{{", "}}", 0, 1, []interface{}{}, forceRaw, partials, template.HTMLEscapeString, nil}
	err := tmpl.parse()

	if err != nil {
		return nil, err
	}

	return &tmpl, err
}

// Render compiles a mustache template string and uses the the given data source
// - generally a map or struct - to render the template and return the output.
func Render(data string, context ...interface{}) (string, error) {
	return RenderRaw(data, false, context...)
}

// RenderRaw compiles a mustache template string and uses the the given data
// source - generally a map or struct - to render the template and return the
// output.
func RenderRaw(data string, forceRaw bool, context ...interface{}) (string, error) {
	return RenderPartialsRaw(data, nil, forceRaw, context...)
}

// RenderPartials compiles a mustache template string and uses the the given partial
// provider and data source - generally a map or struct - to render the template
// and return the output.
func RenderPartials(data string, partials PartialProvider, context ...interface{}) (string, error) {
	return RenderPartialsRaw(data, partials, false, context...)
}

// RenderPartialsRaw compiles a mustache template string and uses the the given
// partial provider and data source - generally a map or struct - to render the
// template and return the output.
func RenderPartialsRaw(data string, partials PartialProvider, forceRaw bool, context ...interface{}) (string, error) {
	var tmpl *Template
	var err error
	if partials == nil {
		tmpl, err = ParseStringRaw(data, forceRaw)
	} else {
		tmpl, err = ParseStringPartialsRaw(data, partials, forceRaw)
	}
	if err != nil {
		return "", err
	}
	return tmpl.Render(context...)
}

// RenderInLayout compiles a mustache template string and layout "wrapper" and
// uses the given data source - generally a map or struct - to render the
// compiled templates and return the output.
func RenderInLayout(data string, layoutData string, context ...interface{}) (string, error) {
	return RenderInLayoutPartials(data, layoutData, nil, context...)
}

// RenderInLayoutPartials compiles a mustache template string and layout
// "wrapper" and uses the given data source - generally a map or struct - to
// render the compiled templates and return the output.
func RenderInLayoutPartials(data string, layoutData string, partials PartialProvider, context ...interface{}) (string, error) {
	var layoutTmpl, tmpl *Template
	var err error
	if partials == nil {
		layoutTmpl, err = ParseString(layoutData)
	} else {
		layoutTmpl, err = ParseStringPartials(layoutData, partials)
	}
	if err != nil {
		return "", err
	}

	if partials == nil {
		tmpl, err = ParseString(data)
	} else {
		tmpl, err = ParseStringPartials(data, partials)
	}

	if err != nil {
		return "", err
	}

	return tmpl.RenderInLayout(layoutTmpl, context...)
}
