// Command gen reads the Dimail OpenAPI document and emits the typed models
// (models_gen.go), the request methods (client_gen.go) and a smoke test that
// exercises every generated method (client_gen_smoke_test.go).
//
// It handles the constructs the Dimail spec actually uses: object/enum/scalar
// schemas, nullable fields expressed as anyOf[T, null], arrays, string enums,
// UUID-formatted strings, additionalProperties maps, path and query parameters,
// JSON request bodies, and object/array/scalar/no-content responses. It is a
// build-time tool invoked through "go generate ./..."; it is not part of the
// shipped library.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ---- OpenAPI subset ----------------------------------------------------------

type doc struct {
	Info struct {
		Title       string `json:"title"`
		Version     string `json:"version"`
		Description string `json:"description"`
	} `json:"info"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components struct {
		Schemas map[string]*schema `json:"schemas"`
	} `json:"components"`
}

type operation struct {
	OperationID string   `json:"operationId"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Parameters  []param  `json:"parameters"`
	RequestBody *reqBody `json:"requestBody"`
	Responses   map[string]struct {
		Content map[string]struct {
			Schema *schema `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

type param struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Required bool    `json:"required"`
	Schema   *schema `json:"schema"`
}

type reqBody struct {
	Required bool `json:"required"`
	Content  map[string]struct {
		Schema *schema `json:"schema"`
	} `json:"content"`
}

type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 json.RawMessage    `json:"type"`
	Format               string             `json:"format"`
	Enum                 []json.RawMessage  `json:"enum"`
	Items                *schema            `json:"items"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	AnyOf                []*schema          `json:"anyOf"`
	AdditionalProperties json.RawMessage    `json:"additionalProperties"`
	Description          string             `json:"description"`
	Title                string             `json:"title"`
}

func (s *schema) types() []string {
	if len(s.Type) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(s.Type, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(s.Type, &many)
	return many
}

func (s *schema) isNull() bool {
	t := s.types()
	return len(t) == 1 && t[0] == "null"
}

// ---- name helpers ------------------------------------------------------------

var initialisms = map[string]bool{
	"ID": true, "UUID": true, "URL": true, "URI": true, "API": true, "HTTP": true,
	"MX": true, "IMAP": true, "SMTP": true, "ACL": true, "ACLS": true, "SPF": true,
	"DKIM": true, "OX": true, "DNS": true, "TTL": true, "IP": true, "JSON": true,
	"DB": true, "OK": true, "OIDC": true,
}

func splitWords(s string) []string {
	var words []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if r == '_' || r == '-' || r == ' ' || r == '.' || r == '@' || r == '/' {
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return words
}

func fixWord(w string) string {
	if initialisms[strings.ToUpper(w)] {
		return strings.ToUpper(w)
	}
	r := []rune(w)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func exported(s string) string {
	var b strings.Builder
	for _, w := range splitWords(s) {
		b.WriteString(fixWord(w))
	}
	return b.String()
}

func unexported(s string) string {
	r := []rune(exported(s))
	if len(r) == 0 {
		return "_"
	}
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) {
		i++
	}
	switch {
	case i == 0:
		// already lower-led
	case i == len(r):
		for j := range r {
			r[j] = unicode.ToLower(r[j])
		}
	default:
		if i > 1 {
			i-- // keep the last uppercase as the start of the next word
		}
		for j := 0; j < i; j++ {
			r[j] = unicode.ToLower(r[j])
		}
	}
	out := string(r)
	if goKeywords[out] {
		out += "_"
	}
	return out
}

var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

func refName(ref string) string {
	i := strings.LastIndex(ref, "/")
	return ref[i+1:]
}

// ---- type resolution ---------------------------------------------------------

type generator struct {
	d       *doc
	schemas map[string]*schema
	// scalarAlias records top-level schemas rendered as `type X = base`.
	scalarAlias map[string]bool
}

// goType resolves a schema to a Go type expression. nullable is true when the
// schema admits JSON null (an anyOf[T, null] wrapper), signalling that an
// optional field should be a pointer.
func (g *generator) goType(s *schema) (typ string, nullable bool) {
	if s == nil {
		return "json.RawMessage", true
	}
	if s.Ref != "" {
		return exported(refName(s.Ref)), false
	}
	if len(s.AnyOf) > 0 {
		var nonNull []*schema
		for _, a := range s.AnyOf {
			if !a.isNull() {
				nonNull = append(nonNull, a)
			}
		}
		if len(nonNull) == 1 {
			t, _ := g.goType(nonNull[0])
			return t, true
		}
		return "json.RawMessage", false
	}
	if len(s.Enum) > 0 {
		return "string", false
	}
	ts := s.types()
	if len(ts) == 1 {
		switch ts[0] {
		case "string":
			return "string", false
		case "integer":
			return "int", false
		case "number":
			return "float64", false
		case "boolean":
			return "bool", false
		case "array":
			et, _ := g.goType(s.Items)
			return "[]" + et, false
		case "object":
			return "map[string]any", false
		case "null":
			return "json.RawMessage", true
		}
	}
	return "json.RawMessage", false
}

// needsPointer reports whether an optional field of the given type should be a
// pointer to distinguish "absent" from "zero". Slices, maps and raw JSON are
// already nilable.
func needsPointer(typ string) bool {
	return !strings.HasPrefix(typ, "[]") &&
		!strings.HasPrefix(typ, "map[") &&
		typ != "json.RawMessage" &&
		typ != "any"
}

func isScalarType(typ string) bool {
	switch typ {
	case "string", "int", "float64", "bool":
		return true
	}
	return false
}

// ---- model generation --------------------------------------------------------

func (g *generator) classify() {
	g.scalarAlias = map[string]bool{}
	for name, s := range g.schemas {
		if len(s.Enum) > 0 || len(s.Properties) > 0 {
			continue
		}
		// No properties and not an enum: an object-map, a scalar or an anyOf
		// union. All render as `type Name = <resolved>`.
		g.scalarAlias[name] = true
	}
}

func docComment(name, desc string) string {
	first := name
	if desc != "" {
		line := strings.TrimSpace(strings.SplitN(desc, "\n", 2)[0])
		line = strings.TrimSuffix(line, ".")
		if line != "" {
			first = name + " " + lowerFirst(line) + "."
		}
	}
	return "// " + first + "\n"
}

func lowerFirst(s string) string {
	r := []rune(s)
	// Do not de-capitalise acronyms or names that start a proper noun run.
	if len(r) >= 2 && unicode.IsUpper(r[1]) {
		return s
	}
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func (g *generator) genModels() []byte {
	var b strings.Builder
	for _, name := range sortedKeys(g.schemas) {
		s := g.schemas[name]
		gn := exported(name)
		switch {
		case len(s.Enum) > 0:
			b.WriteString(g.enumDecl(gn, s))
		case len(s.Properties) > 0:
			b.WriteString(g.structDecl(gn, s))
		default:
			base, _ := g.goType(s)
			b.WriteString(docComment(gn, s.Description))
			fmt.Fprintf(&b, "type %s = %s\n\n", gn, base)
		}
	}
	return g.file("models_gen.go", "", b.String())
}

func (g *generator) enumDecl(gn string, s *schema) string {
	var b strings.Builder
	b.WriteString(docComment(gn, s.Description))
	fmt.Fprintf(&b, "type %s string\n\n", gn)
	b.WriteString("const (\n")
	for _, e := range s.Enum {
		var v string
		if json.Unmarshal(e, &v) != nil {
			continue
		}
		fmt.Fprintf(&b, "\t%s%s %s = %s\n", gn, exported(v), gn, strconv.Quote(v))
	}
	b.WriteString(")\n\n")
	return b.String()
}

func (g *generator) structDecl(gn string, s *schema) string {
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	var b strings.Builder
	b.WriteString(docComment(gn, s.Description))
	fmt.Fprintf(&b, "type %s struct {\n", gn)
	for _, pn := range sortedKeys(s.Properties) {
		typ, nullable := g.goType(s.Properties[pn])
		optional := !req[pn]
		tag := pn
		if optional {
			tag += ",omitempty"
		}
		ft := typ
		if (nullable || optional) && needsPointer(typ) {
			ft = "*" + typ
		}
		fmt.Fprintf(&b, "\t%s %s `json:%q`\n", exported(pn), ft, tag)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// ---- client generation -------------------------------------------------------

var methodOrder = map[string]int{"get": 0, "post": 1, "patch": 2, "put": 3, "delete": 4}

var pathParamRE = regexp.MustCompile(`\{([^}]+)\}`)

type endpoint struct {
	method string
	path   string
	op     *operation
}

func (g *generator) endpoints() []endpoint {
	var eps []endpoint
	for p, methods := range g.d.Paths {
		for m, op := range methods {
			eps = append(eps, endpoint{method: m, path: p, op: op})
		}
	}
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].path != eps[j].path {
			return eps[i].path < eps[j].path
		}
		return methodOrder[eps[i].method] < methodOrder[eps[j].method]
	})
	return eps
}

// resolved describes the shape a generated method returns.
type retKind int

const (
	retVoid retKind = iota
	retJSON          // *T
	retList          // []T
	retValue         // T (scalar or map)
)

type methodPlan struct {
	name       string
	comment    string
	httpMethod string
	pathTmpl   string
	pathParams []param // in path order
	queryPar   []param
	bodyType   string // "" if none
	retKind    retKind
	retType    string // element type for list; full type for json/value
}

func (g *generator) plan(ep endpoint) methodPlan {
	op := ep.op
	mp := methodPlan{
		name:       exported(op.OperationID),
		httpMethod: strings.ToUpper(ep.method),
		pathTmpl:   ep.path,
	}
	// Comment from summary, falling back to the operationId.
	summary := op.Summary
	if summary == "" {
		summary = op.OperationID
	}
	mp.comment = fmt.Sprintf("// %s issues %s %s: %s.\n",
		mp.name, mp.httpMethod, ep.path, strings.TrimSuffix(summary, "."))

	// Path params, ordered by their appearance in the template.
	byName := map[string]param{}
	for _, pr := range op.Parameters {
		switch pr.In {
		case "path":
			byName[pr.Name] = pr
		case "query":
			mp.queryPar = append(mp.queryPar, pr)
		}
	}
	for _, m := range pathParamRE.FindAllStringSubmatch(ep.path, -1) {
		if pr, ok := byName[m[1]]; ok {
			mp.pathParams = append(mp.pathParams, pr)
		} else {
			mp.pathParams = append(mp.pathParams, param{Name: m[1], Schema: &schema{}})
		}
	}

	// Request body.
	if op.RequestBody != nil {
		if c, ok := op.RequestBody.Content["application/json"]; ok && c.Schema != nil {
			bt, _ := g.goType(c.Schema)
			if needsPointer(bt) {
				bt = "*" + bt
			}
			mp.bodyType = bt
		}
	}

	// Success response.
	mp.retKind, mp.retType = g.responseShape(op)
	return mp
}

func (g *generator) responseShape(op *operation) (retKind, string) {
	var sch *schema
	for _, code := range []string{"200", "201", "202", "204"} {
		if r, ok := op.Responses[code]; ok {
			if c, ok := r.Content["application/json"]; ok {
				sch = c.Schema
			}
			break
		}
	}
	if sch == nil {
		return retVoid, ""
	}
	typ, _ := g.goType(sch)
	switch {
	case strings.HasPrefix(typ, "[]"):
		return retList, typ[2:]
	case isScalarType(typ), strings.HasPrefix(typ, "map["):
		return retValue, typ
	default:
		return retJSON, typ
	}
}

func (g *generator) genClient() []byte {
	var b strings.Builder
	needURL := false
	needStrconv := false
	for _, ep := range g.endpoints() {
		mp := g.plan(ep)
		if len(mp.queryPar) > 0 {
			needURL = true
		}
		for _, q := range mp.queryPar {
			t, _ := g.goType(q.Schema)
			if t == "bool" || t == "int" {
				needStrconv = true
			}
		}
		b.WriteString(g.method(mp))
	}
	imports := "\t\"context\"\n"
	if strings.Contains(b.String(), "json.RawMessage") {
		imports += "\t\"encoding/json\"\n"
	}
	if needURL {
		imports += "\t\"net/url\"\n"
	}
	if needStrconv {
		imports += "\t\"strconv\"\n"
	}
	return g.file("client_gen.go", imports, b.String())
}

func (g *generator) method(mp methodPlan) string {
	var b strings.Builder
	b.WriteString(mp.comment)

	// Signature.
	args := []string{"ctx context.Context"}
	pathArg := map[string]string{}
	for _, pp := range mp.pathParams {
		v := unexported(pp.Name)
		pathArg[pp.Name] = v
		args = append(args, v+" string")
	}
	for _, q := range mp.queryPar {
		t, _ := g.goType(q.Schema)
		if !isScalarType(t) {
			t = "string"
		}
		args = append(args, unexported(q.Name)+" *"+t)
	}
	if mp.bodyType != "" {
		args = append(args, "body "+mp.bodyType)
	}

	ret := g.retSig(mp)
	fmt.Fprintf(&b, "func (c *Client) %s(%s) %s {\n", mp.name, strings.Join(args, ", "), ret)

	// Query assembly.
	queryExpr := "nil"
	if len(mp.queryPar) > 0 {
		queryExpr = "q"
		b.WriteString("\tq := url.Values{}\n")
		for _, q := range mp.queryPar {
			t, _ := g.goType(q.Schema)
			v := unexported(q.Name)
			fmt.Fprintf(&b, "\tif %s != nil {\n", v)
			switch t {
			case "bool":
				fmt.Fprintf(&b, "\t\tq.Set(%q, strconv.FormatBool(*%s))\n", q.Name, v)
			case "int":
				fmt.Fprintf(&b, "\t\tq.Set(%q, strconv.Itoa(*%s))\n", q.Name, v)
			default:
				fmt.Fprintf(&b, "\t\tq.Set(%q, *%s)\n", q.Name, v)
			}
			b.WriteString("\t}\n")
		}
	}

	bodyExpr := "nil"
	if mp.bodyType != "" {
		bodyExpr = "body"
	}
	pathExpr := g.pathExpr(mp.pathTmpl, pathArg)

	switch mp.retKind {
	case retVoid:
		fmt.Fprintf(&b, "\treturn doVoid(c, ctx, %q, %s, %s, %s)\n",
			mp.httpMethod, pathExpr, queryExpr, bodyExpr)
	case retList:
		fmt.Fprintf(&b, "\treturn doList[%s](c, ctx, %q, %s, %s, %s)\n",
			mp.retType, mp.httpMethod, pathExpr, queryExpr, bodyExpr)
	case retValue:
		fmt.Fprintf(&b, "\treturn doValue[%s](c, ctx, %q, %s, %s, %s)\n",
			mp.retType, mp.httpMethod, pathExpr, queryExpr, bodyExpr)
	default: // retJSON
		fmt.Fprintf(&b, "\treturn doJSON[%s](c, ctx, %q, %s, %s, %s)\n",
			mp.retType, mp.httpMethod, pathExpr, queryExpr, bodyExpr)
	}
	b.WriteString("}\n\n")
	return b.String()
}

func (g *generator) retSig(mp methodPlan) string {
	switch mp.retKind {
	case retVoid:
		return "error"
	case retList:
		return "([]" + mp.retType + ", error)"
	case retValue:
		return "(" + mp.retType + ", error)"
	default:
		return "(*" + mp.retType + ", error)"
	}
}

// pathExpr builds a Go string expression for a path template, escaping every
// interpolated parameter with esc().
func (g *generator) pathExpr(tmpl string, argFor map[string]string) string {
	var parts []string
	idx := 0
	for _, m := range pathParamRE.FindAllStringSubmatchIndex(tmpl, -1) {
		if lit := tmpl[idx:m[0]]; lit != "" {
			parts = append(parts, strconv.Quote(lit))
		}
		parts = append(parts, "esc("+argFor[tmpl[m[2]:m[3]]]+")")
		idx = m[1]
	}
	if lit := tmpl[idx:]; lit != "" {
		parts = append(parts, strconv.Quote(lit))
	}
	if len(parts) == 0 {
		return strconv.Quote(tmpl)
	}
	return strings.Join(parts, " + ")
}

// ---- smoke test generation ---------------------------------------------------

// genSmoke emits a test that calls every generated method against an httptest
// server, guaranteeing statement coverage of client_gen.go. Query methods are
// called twice (populated and nil) to cover both parameter-encoding branches.
func (g *generator) genSmoke() []byte {
	var calls strings.Builder
	for _, ep := range g.endpoints() {
		mp := g.plan(ep)
		g.smokeCall(&calls, mp, false)
		if len(mp.queryPar) > 0 {
			g.smokeCall(&calls, mp, true)
		}
	}
	body := smokeHeader + calls.String() + smokeFooter
	return g.file("client_gen_smoke_test.go", smokeImports, body)
}

func (g *generator) smokeCall(b *strings.Builder, mp methodPlan, nilQuery bool) {
	// Response body the mock server should return for this call.
	switch mp.retKind {
	case retList:
		fmt.Fprintf(b, "\tsrv.body = `[]`\n")
	case retVoid:
		fmt.Fprintf(b, "\tsrv.body = ``\n")
	case retValue:
		fmt.Fprintf(b, "\tsrv.body = %s\n", valueBody(mp.retType))
	default:
		fmt.Fprintf(b, "\tsrv.body = `{}`\n")
	}
	args := []string{"ctx"}
	for range mp.pathParams {
		args = append(args, `"x"`)
	}
	for _, q := range mp.queryPar {
		if nilQuery {
			args = append(args, "nil")
			continue
		}
		t, _ := g.goType(q.Schema)
		switch t {
		case "bool":
			args = append(args, "boolPtr(true)")
		case "int":
			args = append(args, "intPtr(1)")
		default:
			args = append(args, `strPtr("x")`)
		}
	}
	if mp.bodyType != "" {
		args = append(args, zeroBody(mp.bodyType))
	}
	lhs := "_, _ ="
	if mp.retKind == retVoid {
		lhs = "_ ="
	}
	fmt.Fprintf(b, "\t%s c.%s(%s)\n", lhs, mp.name, strings.Join(args, ", "))
}

func valueBody(typ string) string {
	switch typ {
	case "bool":
		return "`false`"
	case "int", "float64":
		return "`0`"
	case "string":
		return "`\"\"`"
	default: // map[string]any or similar
		return "`{}`"
	}
}

// zeroBody returns an expression constructing a zero value of a body type.
// Body types are always pointers to named request structs (e.g. *CreateUser).
func zeroBody(typ string) string {
	if strings.HasPrefix(typ, "*") {
		return "&" + typ[1:] + "{}"
	}
	return typ + "{}"
}

// ---- file assembly -----------------------------------------------------------

func (g *generator) file(name, imports, body string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by internal/gen from openapi.json (Dimail API %s); DO NOT EDIT.\n\n",
		g.d.Info.Version)
	b.WriteString("package dimail\n\n")
	if imports == "" {
		// models_gen.go only needs encoding/json when json.RawMessage appears.
		if strings.Contains(body, "json.RawMessage") {
			b.WriteString("import \"encoding/json\"\n\n")
		}
	} else {
		b.WriteString("import (\n")
		b.WriteString(imports)
		b.WriteString(")\n\n")
	}
	b.WriteString(body)
	src, err := format.Source(b.Bytes())
	if err != nil {
		// Emit the unformatted source to aid debugging, then fail.
		fmt.Fprintln(os.Stderr, b.String())
		log.Fatalf("format %s: %v", name, err)
	}
	return src
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---- smoke test boilerplate --------------------------------------------------

const smokeImports = "\t\"context\"\n\t\"net/http\"\n\t\"net/http/httptest\"\n\t\"testing\"\n"

const smokeHeader = `type smokeServer struct{ body string }

func (s *smokeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(s.body))
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }

// TestGeneratedMethodsSmoke calls every generated method against a mock server,
// covering the single delegating statement (and both query-encoding branches)
// of each. Behavioural assertions live in client_test.go.
func TestGeneratedMethodsSmoke(t *testing.T) {
	srv := &smokeServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithToken("t"))
	ctx := context.Background()
`

const smokeFooter = "}\n"

// ---- entry point -------------------------------------------------------------

func main() {
	spec := flag.String("spec", "openapi.json", "path to the OpenAPI document")
	out := flag.String("out", ".", "output directory for generated files")
	flag.Parse()

	raw, err := os.ReadFile(*spec)
	if err != nil {
		log.Fatalf("read spec: %v", err)
	}
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		log.Fatalf("parse spec: %v", err)
	}
	g := &generator{d: &d, schemas: d.Components.Schemas}
	g.classify()

	files := map[string][]byte{
		"models_gen.go":             g.genModels(),
		"client_gen.go":             g.genClient(),
		"client_gen_smoke_test.go":  g.genSmoke(),
	}
	for name, src := range files {
		p := filepath.Join(*out, name)
		if err := os.WriteFile(p, src, 0o644); err != nil {
			log.Fatalf("write %s: %v", name, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", p, len(src))
	}
}
