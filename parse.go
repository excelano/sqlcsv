package main

import (
	"fmt"
	"strings"
)

// AST root.

type Stmt interface{ stmt() }

func (*SelectStmt) stmt() {}
func (*UpdateStmt) stmt() {}
func (*DeleteStmt) stmt() {}
func (*InsertStmt) stmt() {}

// SelectStmt represents a SELECT. Star is true for `SELECT *`; in that case
// Columns is nil.
type SelectStmt struct {
	Star    bool
	Columns []string
	Where   Predicate
}

type UpdateStmt struct {
	Assignments []Assignment
	Where       Predicate
}

type DeleteStmt struct {
	Where Predicate
}

type InsertStmt struct {
	Columns []string
	Values  []Value
}

type Assignment struct {
	Column string
	Value  Value
}

// Predicate is the WHERE tree.

type Predicate interface{ predicate() }

func (*BinaryOp) predicate()   {}
func (*NotOp) predicate()      {}
func (*Comparison) predicate() {}
func (*NullTest) predicate()   {}

// BinaryOp is "AND" or "OR".
type BinaryOp struct {
	Op string
	L  Predicate
	R  Predicate
}

type NotOp struct {
	Inner Predicate
}

// Comparison: column op literal. Op is one of "=", "!=", "<", "<=", ">", ">=".
type Comparison struct {
	Column string
	Op     string
	Value  Value
}

// NullTest: column IS [NOT] NULL.
type NullTest struct {
	Column string
	Not    bool
}

// Value is a literal. Kind selects which field is meaningful.
type Value struct {
	Kind ValueKind
	Str  string
	Num  string
	Bool bool
}

type ValueKind int

const (
	ValString ValueKind = iota
	ValNumber
	ValBool
	ValNull
)

// ParseError carries a byte-offset position to enable caret-style error
// rendering in later phases.
type ParseError struct {
	Msg string
	Pos int
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at offset %d: %s", e.Pos, e.Msg)
}

func parseErrorAt(pos int, msg string) *ParseError {
	return &ParseError{Msg: msg, Pos: pos}
}

// PreProcess strips REPL conveniences (trailing ";" and "!") from input
// before it reaches the parser. The bool return is the "skip prompt / commit
// immediately" signal carried by a trailing "!". Both suffixes may appear in
// either order and are stripped iteratively.
func PreProcess(input string) (string, bool) {
	s := strings.TrimSpace(input)
	commit := false
	for {
		changed := false
		if strings.HasSuffix(s, ";") {
			s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
			changed = true
		}
		if strings.HasSuffix(s, "!") {
			commit = true
			s = strings.TrimSpace(strings.TrimSuffix(s, "!"))
			changed = true
		}
		if !changed {
			break
		}
	}
	return s, commit
}

// Parse turns a SQL statement string into its AST. Input must be pre-processed
// (trailing ";" / "!" stripped) — call PreProcess first when coming from the
// REPL or --exec.
func Parse(input string) (Stmt, error) {
	p, err := newParser(input)
	if err != nil {
		return nil, err
	}
	return p.parseStatement()
}

// Lexer.

type TokenType int

const (
	TokEOF TokenType = iota
	TokIdent
	TokQuotedIdent
	TokString
	TokNumber
	TokStar
	TokLParen
	TokRParen
	TokComma
	TokEq
	TokNe
	TokLt
	TokLe
	TokGt
	TokGe
	TokSelect
	TokUpdate
	TokDelete
	TokInsert
	TokSet
	TokValues
	TokWhere
	TokAnd
	TokOr
	TokNot
	TokIs
	TokNull
	TokTrue
	TokFalse
)

type Token struct {
	Type TokenType
	Lit  string
	Pos  int
}

var keywords = map[string]TokenType{
	"SELECT": TokSelect,
	"UPDATE": TokUpdate,
	"DELETE": TokDelete,
	"INSERT": TokInsert,
	"SET":    TokSet,
	"VALUES": TokValues,
	"WHERE":  TokWhere,
	"AND":    TokAnd,
	"OR":     TokOr,
	"NOT":    TokNot,
	"IS":     TokIs,
	"NULL":   TokNull,
	"TRUE":   TokTrue,
	"FALSE":  TokFalse,
}

type lexer struct {
	src string
	pos int
}

func (l *lexer) skipWhitespace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			l.pos++
			continue
		}
		break
	}
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func (l *lexer) next() (Token, error) {
	l.skipWhitespace()
	if l.pos >= len(l.src) {
		return Token{Type: TokEOF, Pos: l.pos}, nil
	}
	start := l.pos
	c := l.src[l.pos]
	switch {
	case c == '\'':
		return l.lexString(start)
	case c == '"':
		return l.lexQuotedIdent(start)
	case c == '-' || isDigit(c):
		return l.lexNumber(start)
	case isLetter(c) || c == '_':
		return l.lexIdent(start)
	}

	switch c {
	case '*':
		l.pos++
		return Token{Type: TokStar, Lit: "*", Pos: start}, nil
	case '(':
		l.pos++
		return Token{Type: TokLParen, Lit: "(", Pos: start}, nil
	case ')':
		l.pos++
		return Token{Type: TokRParen, Lit: ")", Pos: start}, nil
	case ',':
		l.pos++
		return Token{Type: TokComma, Lit: ",", Pos: start}, nil
	case '=':
		l.pos++
		return Token{Type: TokEq, Lit: "=", Pos: start}, nil
	case '!':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: TokNe, Lit: "!=", Pos: start}, nil
		}
		return Token{}, parseErrorAt(start, "expected '=' after '!'")
	case '<':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: TokLe, Lit: "<=", Pos: start}, nil
		}
		l.pos++
		return Token{Type: TokLt, Lit: "<", Pos: start}, nil
	case '>':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '=' {
			l.pos += 2
			return Token{Type: TokGe, Lit: ">=", Pos: start}, nil
		}
		l.pos++
		return Token{Type: TokGt, Lit: ">", Pos: start}, nil
	}

	return Token{}, parseErrorAt(start, fmt.Sprintf("unexpected character %q", c))
}

func (l *lexer) lexString(start int) (Token, error) {
	l.pos++ // consume opening '
	var sb strings.Builder
	for {
		if l.pos >= len(l.src) {
			return Token{}, parseErrorAt(start, "unterminated string literal")
		}
		c := l.src[l.pos]
		if c == '\'' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '\'' {
				sb.WriteByte('\'')
				l.pos += 2
				continue
			}
			l.pos++
			return Token{Type: TokString, Lit: sb.String(), Pos: start}, nil
		}
		sb.WriteByte(c)
		l.pos++
	}
}

func (l *lexer) lexQuotedIdent(start int) (Token, error) {
	l.pos++ // consume opening "
	var sb strings.Builder
	for {
		if l.pos >= len(l.src) {
			return Token{}, parseErrorAt(start, "unterminated quoted identifier")
		}
		c := l.src[l.pos]
		if c == '"' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '"' {
				sb.WriteByte('"')
				l.pos += 2
				continue
			}
			l.pos++
			if sb.Len() == 0 {
				return Token{}, parseErrorAt(start, "empty quoted identifier")
			}
			return Token{Type: TokQuotedIdent, Lit: sb.String(), Pos: start}, nil
		}
		sb.WriteByte(c)
		l.pos++
	}
}

func (l *lexer) lexNumber(start int) (Token, error) {
	if l.src[l.pos] == '-' {
		l.pos++
		if l.pos >= len(l.src) || !isDigit(l.src[l.pos]) {
			return Token{}, parseErrorAt(start, "expected digit after '-'")
		}
	}
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		if l.pos >= len(l.src) || !isDigit(l.src[l.pos]) {
			return Token{}, parseErrorAt(start, "expected digit after '.'")
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	return Token{Type: TokNumber, Lit: l.src[start:l.pos], Pos: start}, nil
}

func (l *lexer) lexIdent(start int) (Token, error) {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isLetter(c) || isDigit(c) || c == '_' {
			l.pos++
			continue
		}
		break
	}
	lit := l.src[start:l.pos]
	if kw, ok := keywords[strings.ToUpper(lit)]; ok {
		return Token{Type: kw, Lit: lit, Pos: start}, nil
	}
	return Token{Type: TokIdent, Lit: lit, Pos: start}, nil
}

// Parser.

type parser struct {
	tokens []Token
	pos    int
}

func newParser(input string) (*parser, error) {
	l := &lexer{src: input}
	var toks []Token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.Type == TokEOF {
			break
		}
	}
	return &parser{tokens: toks}, nil
}

func (p *parser) peek() Token { return p.tokens[p.pos] }

func (p *parser) advance() Token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

func (p *parser) accept(tt TokenType) (Token, bool) {
	if p.peek().Type == tt {
		return p.advance(), true
	}
	return Token{}, false
}

func (p *parser) expect(tt TokenType, what string) (Token, error) {
	if t, ok := p.accept(tt); ok {
		return t, nil
	}
	got := p.peek()
	return Token{}, parseErrorAt(got.Pos, fmt.Sprintf("expected %s, got %s", what, describeToken(got)))
}

func (p *parser) expectEOF() error {
	if p.peek().Type != TokEOF {
		t := p.peek()
		return parseErrorAt(t.Pos, fmt.Sprintf("unexpected %s after end of statement", describeToken(t)))
	}
	return nil
}

func (p *parser) parseStatement() (Stmt, error) {
	switch p.peek().Type {
	case TokSelect:
		p.advance()
		return p.parseSelectBody()
	case TokUpdate:
		p.advance()
		return p.parseUpdateBody()
	case TokDelete:
		p.advance()
		return p.parseDeleteBody()
	case TokInsert:
		p.advance()
		return p.parseInsertBody()
	case TokEOF:
		return nil, parseErrorAt(p.peek().Pos, "empty input")
	default:
		t := p.peek()
		return nil, parseErrorAt(t.Pos, fmt.Sprintf("expected SELECT, UPDATE, DELETE, or INSERT, got %s", describeToken(t)))
	}
}

func (p *parser) parseSelectBody() (Stmt, error) {
	sel := &SelectStmt{}
	if _, ok := p.accept(TokStar); ok {
		sel.Star = true
	} else {
		cols, err := p.parseColumnList()
		if err != nil {
			return nil, err
		}
		sel.Columns = cols
	}
	if _, ok := p.accept(TokWhere); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		sel.Where = pred
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return sel, nil
}

func (p *parser) parseUpdateBody() (Stmt, error) {
	if _, err := p.expect(TokSet, "SET"); err != nil {
		return nil, err
	}
	first, err := p.parseAssignment()
	if err != nil {
		return nil, err
	}
	assigns := []Assignment{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		a, err := p.parseAssignment()
		if err != nil {
			return nil, err
		}
		assigns = append(assigns, a)
	}
	upd := &UpdateStmt{Assignments: assigns}
	if _, ok := p.accept(TokWhere); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		upd.Where = pred
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return upd, nil
}

func (p *parser) parseAssignment() (Assignment, error) {
	col, err := p.parseColumn()
	if err != nil {
		return Assignment{}, err
	}
	if _, err := p.expect(TokEq, "'='"); err != nil {
		return Assignment{}, err
	}
	v, err := p.parseValue()
	if err != nil {
		return Assignment{}, err
	}
	return Assignment{Column: col, Value: v}, nil
}

func (p *parser) parseDeleteBody() (Stmt, error) {
	del := &DeleteStmt{}
	if _, ok := p.accept(TokWhere); ok {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		del.Where = pred
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return del, nil
}

func (p *parser) parseInsertBody() (Stmt, error) {
	if _, err := p.expect(TokLParen, "'('"); err != nil {
		return nil, err
	}
	cols, err := p.parseColumnList()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	if _, err := p.expect(TokValues, "VALUES"); err != nil {
		return nil, err
	}
	if _, err := p.expect(TokLParen, "'('"); err != nil {
		return nil, err
	}
	first, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	values := []Value{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	if _, err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	if err := p.expectEOF(); err != nil {
		return nil, err
	}
	return &InsertStmt{Columns: cols, Values: values}, nil
}

func (p *parser) parseColumnList() ([]string, error) {
	first, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	cols := []string{first}
	for {
		if _, ok := p.accept(TokComma); !ok {
			break
		}
		c, err := p.parseColumn()
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, nil
}

func (p *parser) parseColumn() (string, error) {
	t := p.peek()
	if t.Type == TokIdent || t.Type == TokQuotedIdent {
		p.advance()
		return t.Lit, nil
	}
	return "", parseErrorAt(t.Pos, fmt.Sprintf("expected column name, got %s", describeToken(t)))
}

func (p *parser) parseValue() (Value, error) {
	t := p.peek()
	switch t.Type {
	case TokString:
		p.advance()
		return Value{Kind: ValString, Str: t.Lit}, nil
	case TokNumber:
		p.advance()
		return Value{Kind: ValNumber, Num: t.Lit}, nil
	case TokTrue:
		p.advance()
		return Value{Kind: ValBool, Bool: true}, nil
	case TokFalse:
		p.advance()
		return Value{Kind: ValBool, Bool: false}, nil
	case TokNull:
		p.advance()
		return Value{Kind: ValNull}, nil
	}
	return Value{}, parseErrorAt(t.Pos, fmt.Sprintf("expected literal value, got %s", describeToken(t)))
}

func (p *parser) parsePredicate() (Predicate, error) {
	return p.parseDisjunction()
}

func (p *parser) parseDisjunction() (Predicate, error) {
	left, err := p.parseConjunction()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.accept(TokOr); !ok {
			break
		}
		right, err := p.parseConjunction()
		if err != nil {
			return nil, err
		}
		left = &BinaryOp{Op: "OR", L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseConjunction() (Predicate, error) {
	left, err := p.parseNegation()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.accept(TokAnd); !ok {
			break
		}
		right, err := p.parseNegation()
		if err != nil {
			return nil, err
		}
		left = &BinaryOp{Op: "AND", L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseNegation() (Predicate, error) {
	if _, ok := p.accept(TokNot); ok {
		inner, err := p.parseNegation()
		if err != nil {
			return nil, err
		}
		return &NotOp{Inner: inner}, nil
	}
	return p.parseAtom()
}

func (p *parser) parseAtom() (Predicate, error) {
	if _, ok := p.accept(TokLParen); ok {
		inner, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRParen, "')'"); err != nil {
			return nil, err
		}
		return inner, nil
	}
	col, err := p.parseColumn()
	if err != nil {
		return nil, err
	}
	if _, ok := p.accept(TokIs); ok {
		not := false
		if _, ok := p.accept(TokNot); ok {
			not = true
		}
		if _, err := p.expect(TokNull, "NULL"); err != nil {
			return nil, err
		}
		return &NullTest{Column: col, Not: not}, nil
	}
	opTok := p.peek()
	var op string
	switch opTok.Type {
	case TokEq:
		op = "="
	case TokNe:
		op = "!="
	case TokLt:
		op = "<"
	case TokLe:
		op = "<="
	case TokGt:
		op = ">"
	case TokGe:
		op = ">="
	default:
		return nil, parseErrorAt(opTok.Pos, fmt.Sprintf("expected comparison operator or IS, got %s", describeToken(opTok)))
	}
	p.advance()
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if v.Kind == ValNull {
		return nil, parseErrorAt(opTok.Pos, fmt.Sprintf("cannot use '%s' with NULL; use IS NULL or IS NOT NULL instead", op))
	}
	return &Comparison{Column: col, Op: op, Value: v}, nil
}

func describeToken(t Token) string {
	switch t.Type {
	case TokEOF:
		return "end of input"
	case TokIdent:
		return fmt.Sprintf("identifier %q", t.Lit)
	case TokQuotedIdent:
		return fmt.Sprintf("quoted identifier %q", t.Lit)
	case TokString:
		return fmt.Sprintf("string literal %q", t.Lit)
	case TokNumber:
		return fmt.Sprintf("number %s", t.Lit)
	default:
		return fmt.Sprintf("%q", t.Lit)
	}
}
