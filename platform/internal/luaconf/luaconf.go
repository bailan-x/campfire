// Package luaconf is a deliberately small Lua subset reader/writer.
//
// It exists to round-trip DST's config files (modinfo.lua, modoverrides.lua,
// worldgenoverride.lua, cluster.ini is INI not Lua) without shipping a full
// Lua VM: we only need constants, table constructors and top-level
// `name = value` assignments.
package luaconf

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind enumerates Lua value kinds we support.
type Kind int

const (
	KNil Kind = iota
	KBool
	KNumber
	KString
	KTable
)

// Value is a Lua value: nil, bool, number, string or table.
// Table keeps both array part (List) and string keys (Map).
type Value struct {
	Kind Kind
	Bool bool
	Num  float64
	Str  string
	List []*Value
	Map  map[string]*Value
}

func Nil() *Value           { return &Value{Kind: KNil} }
func Bool(b bool) *Value    { return &Value{Kind: KBool, Bool: b} }
func Number(f float64) *Value { return &Value{Kind: KNumber, Num: f} }
func Str(s string) *Value   { return &Value{Kind: KString, Str: s} }
func Table() *Value         { return &Value{Kind: KTable, Map: map[string]*Value{}} }

// Get returns the value for a string key (nil-safe, returns nil for missing).
func (v *Value) Get(key string) *Value {
	if v == nil || v.Kind != KTable || v.Map == nil {
		return nil
	}
	return v.Map[key]
}

// GetString is a convenience for optional string fields.
func (v *Value) GetString(key string) (string, bool) {
	c := v.Get(key)
	if c == nil || c.Kind != KString {
		return "", false
	}
	return c.Str, true
}

// GetBool is a convenience for optional bool fields.
func (v *Value) GetBool(key string) (bool, bool) {
	c := v.Get(key)
	if c == nil || c.Kind != KBool {
		return false, false
	}
	return c.Bool, true
}

// Set writes a string key.
func (v *Value) Set(key string, val *Value) {
	if v.Map == nil {
		v.Map = map[string]*Value{}
	}
	v.Map[key] = val
}

// Keys returns sorted table keys.
func (v *Value) Keys() []string {
	keys := make([]string, 0, len(v.Map))
	for k := range v.Map {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ToGo converts a value into plain Go types (for JSON encoding).
func (v *Value) ToGo() interface{} {
	if v == nil {
		return nil
	}
	switch v.Kind {
	case KBool:
		return v.Bool
	case KNumber:
		return v.Num
	case KString:
		return v.Str
	case KTable:
		if len(v.List) > 0 && len(v.Map) == 0 {
			out := make([]interface{}, 0, len(v.List))
			for _, it := range v.List {
				out = append(out, it.ToGo())
			}
			return out
		}
		out := make(map[string]interface{}, len(v.Map))
		for k, it := range v.Map {
			out[k] = it.ToGo()
		}
		return out
	default:
		return nil
	}
}

// ---- parser ----

type tokenKind int

const (
	tEOF tokenKind = iota
	tIdent
	tNumber
	tString
	tPunct
	tKeyword
)

type token struct {
	kind tokenKind
	text string
	num  float64
	line int
}

type lexer struct {
	src  string
	pos  int
	line int
	toks []token
}

func lex(src string) ([]token, error) {
	l := &lexer{src: src, line: 1}
	for {
		l.skipSpaceAndComments()
		if l.pos >= len(l.src) {
			l.toks = append(l.toks, token{kind: tEOF, line: l.line})
			return l.toks, nil
		}
		ch := l.src[l.pos]
		switch {
		case ch == '"' || ch == '\'':
			s, err := l.readString(ch)
			if err != nil {
				return nil, err
			}
			l.toks = append(l.toks, token{kind: tString, text: s, line: l.line})
		case isDigit(ch) || (ch == '-' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1])):
			l.toks = append(l.toks, l.readNumber())
		case isIdentStart(ch):
			start := l.pos
			for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
				l.pos++
			}
			word := l.src[start:l.pos]
			if word == "true" || word == "false" || word == "nil" || word == "return" || word == "function" || word == "local" {
				l.toks = append(l.toks, token{kind: tKeyword, text: word, line: l.line})
			} else {
				l.toks = append(l.toks, token{kind: tIdent, text: word, line: l.line})
			}
		case strings.ContainsRune("{}[]=,;()-+/", rune(ch)):
			l.pos++
			l.toks = append(l.toks, token{kind: tPunct, text: string(ch), line: l.line})
		default:
			// Unknown punctuation: skip it rather than fail the whole file.
			l.pos++
		}
	}
}

func (l *lexer) skipSpaceAndComments() {
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == '\n' {
			l.line++
			l.pos++
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' {
			l.pos++
			continue
		}
		if ch == '-' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '-' {
			// line comment or long comment --[[ ]]
			if l.pos+3 < len(l.src) && l.src[l.pos+2] == '[' && l.src[l.pos+3] == '[' {
				end := strings.Index(l.src[l.pos+4:], "]]")
				if end < 0 {
					l.pos = len(l.src)
					return
				}
				l.pos += 4 + end + 2
				continue
			}
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		return
	}
}

func (l *lexer) readString(quote byte) (string, error) {
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == '\\' && l.pos+1 < len(l.src) {
			n := l.src[l.pos+1]
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '"', '\'':
				b.WriteByte(n)
			default:
				b.WriteByte(n)
			}
			l.pos += 2
			continue
		}
		if ch == quote {
			l.pos++
			return b.String(), nil
		}
		if ch == '\n' {
			l.line++
		}
		b.WriteByte(ch)
		l.pos++
	}
	return "", fmt.Errorf("unterminated string at line %d", l.line)
}

func (l *lexer) readNumber() token {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
	}
	for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '.') {
		l.pos++
	}
	f, _ := strconv.ParseFloat(l.src[start:l.pos], 64)
	return token{kind: tNumber, num: f, line: l.line}
}

func isDigit(ch byte) bool      { return ch >= '0' && ch <= '9' }
func isIdentStart(ch byte) bool { return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') }
func isIdentPart(ch byte) bool  { return isIdentStart(ch) || isDigit(ch) }

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token  { return p.toks[p.pos] }
func (p *parser) next() token  { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) accept(text string) bool {
	if p.peek().kind == tPunct && p.peek().text == text {
		p.pos++
		return true
	}
	return false
}
func (p *parser) expect(text string) error {
	if !p.accept(text) {
		return fmt.Errorf("expected %q at line %d, got %q", text, p.peek().line, p.peek().text)
	}
	return nil
}

// Chunk is a parsed Lua file: top-level assignments plus an optional return value.
type Chunk struct {
	Fields map[string]*Value
	Return *Value
}

// Parse parses the subset of Lua used by DST config files.
func Parse(src string) (*Chunk, error) {
	// DST rewrites the files it manages with a "KLEI 1" magic header
	// (e.g. "KLEI     1 return { ... }"); strip it before lexing.
	src = stripKleiHeader(src)
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	chunk := &Chunk{Fields: map[string]*Value{}}
	for {
		t := p.peek()
		if t.kind == tEOF {
			return chunk, nil
		}
		if t.kind == tKeyword && t.text == "return" {
			p.next()
			v, err := p.parseValue()
			if err != nil {
				return chunk, err
			}
			chunk.Return = v
			return chunk, nil
		}
		if t.kind == tKeyword && (t.text == "local" || t.text == "function") {
			// Not needed for config files; skip the statement defensively.
			p.skipStatement()
			continue
		}
		if t.kind == tIdent || (t.kind == tKeyword && t.text == "nil") {
			name := t.text
			p.next()
			// support a.b = ... by keeping only the root key
			for p.accept(".") {
				if p.peek().kind == tIdent {
					name += "." + p.next().text
				}
			}
			if !p.accept("=") {
				p.skipStatement()
				continue
			}
			v, err := p.parseValue()
			if err != nil {
				return chunk, err
			}
			chunk.Fields[name] = v
			continue
		}
		// Anything else: skip one token to guarantee progress.
		p.next()
	}
}

// stripKleiHeader removes the "KLEI <version>" magic prefix DST writes into the
// config files it manages.
func stripKleiHeader(src string) string {
	t := strings.TrimLeft(src, " \t\r\n")
	if !strings.HasPrefix(t, "KLEI") {
		return src
	}
	rest := t[len("KLEI"):]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	j := i
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == i {
		return src
	}
	return strings.TrimLeft(rest[j:], " \t\r\n")
}

func (p *parser) skipStatement() {	depth := 0
	for {
		t := p.peek()
		if t.kind == tEOF {
			return
		}
		if t.kind == tPunct {
			switch t.text {
			case "{", "(", "[":
				depth++
			case "}", ")", "]":
				if depth > 0 {
					depth--
				}
			}
		}
		p.next()
		if depth == 0 && (t.kind == tPunct && (t.text == ";" || t.text == "}")) {
			return
		}
	}
}

func (p *parser) parseValue() (*Value, error) {
	t := p.peek()
	switch t.kind {
	case tString:
		p.next()
		return Str(t.text), nil
	case tNumber:
		p.next()
		return Number(t.num), nil
	case tKeyword:
		switch t.text {
		case "true":
			p.next()
			return Bool(true), nil
		case "false":
			p.next()
			return Bool(false), nil
		case "nil":
			p.next()
			return Nil(), nil
		}
	case tPunct:
		if t.text == "{" {
			return p.parseTable()
		}
		if t.text == "-" {
			p.next()
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			if v.Kind == KNumber {
				return Number(-v.Num), nil
			}
			return v, nil
		}
	case tIdent:
		// Bare identifier (e.g. TUNING.X) — keep as opaque string.
		p.next()
		return Str(t.text), nil
	}
	p.next()
	return Nil(), nil
}

func (p *parser) parseTable() (*Value, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	tv := Table()
	for {
		if p.peek().kind == tEOF {
			return tv, fmt.Errorf("unterminated table at line %d", p.peek().line)
		}
		if p.accept("}") {
			return tv, nil
		}
		// keyed: ident = value | [expr] = value | ["key"] = value
		if p.peek().kind == tIdent && p.toks[p.pos+1].kind == tPunct && p.toks[p.pos+1].text == "=" {
			key := p.next().text
			p.next() // =
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			tv.Set(key, v)
		} else if p.peek().kind == tPunct && p.peek().text == "[" {
			p.next() // [
			k := p.peek()
			p.next()
			key := k.text
			if k.kind == tNumber {
				key = strconv.FormatFloat(k.num, 'f', -1, 64)
			}
			_ = p.expect("]")
			_ = p.expect("=")
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			tv.Set(key, v)
		} else {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			tv.List = append(tv.List, v)
		}
		p.accept(",")
		p.accept(";")
	}
}

// ---- renderer ----

const identSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

func isIdentText(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(identSafe, rune(s[i])) {
			return false
		}
	}
	return !isDigit(s[0])
}

// Quote renders a Lua string literal.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		case '\r':
			b.WriteString("\\r")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Render serialises a value as Lua source. Tables always use the long form,
// which keeps files stable and diff-friendly.
func Render(v *Value) string {
	var b strings.Builder
	renderTo(&b, v, 0)
	return b.String()
}

func renderTo(b *strings.Builder, v *Value, indent int) {
	if v == nil {
		b.WriteString("nil")
		return
	}
	pad := strings.Repeat("\t", indent+1)
	switch v.Kind {
	case KBool:
		if v.Bool {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case KNumber:
		b.WriteString(formatNumber(v.Num))
	case KString:
		b.WriteString(Quote(v.Str))
	case KTable:
		if len(v.List) == 0 && len(v.Map) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for _, item := range v.List {
			b.WriteString(pad)
			renderTo(b, item, indent+1)
			b.WriteString(",\n")
		}
		for _, k := range v.Keys() {
			b.WriteString(pad)
			if isIdentText(k) {
				b.WriteString(k)
			} else {
				b.WriteString("[" + Quote(k) + "]")
			}
			b.WriteString(" = ")
			renderTo(b, v.Map[k], indent+1)
			b.WriteString(",\n")
		}
		b.WriteString(strings.Repeat("\t", indent))
		b.WriteString("}")
	default:
		b.WriteString("nil")
	}
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
