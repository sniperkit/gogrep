// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

type matcher struct {
	values map[string]ast.Node
}

func (m *matcher) node(expr, node ast.Node) bool {
	if expr == nil || node == nil {
		return expr == node
	}
	switch x := expr.(type) {
	case *ast.File:
		y, ok := node.(*ast.File)
		if !ok || !m.node(x.Name, y.Name) || len(x.Decls) != len(y.Decls) ||
			len(x.Imports) != len(y.Imports) {
			return false
		}
		for i, decl := range x.Decls {
			if !m.node(decl, y.Decls[i]) {
				return false
			}
		}
		for i, imp := range x.Imports {
			if !m.node(imp, y.Imports[i]) {
				return false
			}
		}
		return true

	case *ast.Ident:
		if !isWildName(x.Name) {
			// not a wildcard
			y, ok := node.(*ast.Ident)
			return ok && x.Name == y.Name
		}
		if _, ok := node.(ast.Node); !ok {
			return false // to not include our extra node types
		}
		name, any := fromWildName(x.Name)
		if any {
			return false
		}
		if name == "_" {
			// values are discarded, matches anything
			return true
		}
		prev, ok := m.values[name]
		if !ok {
			// first occurrence, record value
			m.values[name] = node
			return true
		}
		// multiple uses must match
		return m.node(prev, node)

	// lists (ys are generated by us while walking)
	case exprList:
		y, ok := node.(exprList)
		return ok && m.nodes(x, y)
	case stmtList:
		y, ok := node.(stmtList)
		return ok && m.nodes(x, y)

	// lits
	case *ast.BasicLit:
		y, ok := node.(*ast.BasicLit)
		return ok && x.Kind == y.Kind && x.Value == y.Value
	case *ast.CompositeLit:
		y, ok := node.(*ast.CompositeLit)
		return ok && m.node(x.Type, y.Type) && m.exprs(x.Elts, y.Elts)
	case *ast.FuncLit:
		y, ok := node.(*ast.FuncLit)
		return ok && m.node(x.Type, y.Type) && m.node(x.Body, y.Body)

	// types
	case *ast.ArrayType:
		y, ok := node.(*ast.ArrayType)
		return ok && m.node(x.Len, y.Len) && m.node(x.Elt, y.Elt)
	case *ast.MapType:
		y, ok := node.(*ast.MapType)
		return ok && m.node(x.Key, y.Key) && m.node(x.Value, y.Value)
	case *ast.StructType:
		y, ok := node.(*ast.StructType)
		return ok && m.fields(x.Fields, y.Fields)
	case *ast.Field:
		// TODO: tags?
		y, ok := node.(*ast.Field)
		return ok && m.idents(x.Names, y.Names) && m.node(x.Type, y.Type)
	case *ast.FuncType:
		y, ok := node.(*ast.FuncType)
		return ok && m.fields(x.Params, y.Params) &&
			m.fields(x.Results, y.Results)
	case *ast.InterfaceType:
		y, ok := node.(*ast.InterfaceType)
		return ok && m.fields(x.Methods, y.Methods)
	case *ast.ChanType:
		y, ok := node.(*ast.ChanType)
		return ok && x.Dir == y.Dir && m.node(x.Value, y.Value)

	// other exprs
	case *ast.Ellipsis:
		y, ok := node.(*ast.Ellipsis)
		return ok && m.node(x.Elt, y.Elt)
	case *ast.ParenExpr:
		y, ok := node.(*ast.ParenExpr)
		return ok && m.node(x.X, y.X)
	case *ast.UnaryExpr:
		y, ok := node.(*ast.UnaryExpr)
		return ok && x.Op == y.Op && m.node(x.X, y.X)
	case *ast.BinaryExpr:
		y, ok := node.(*ast.BinaryExpr)
		return ok && x.Op == y.Op && m.node(x.X, y.X) && m.node(x.Y, y.Y)
	case *ast.CallExpr:
		y, ok := node.(*ast.CallExpr)
		return ok && m.node(x.Fun, y.Fun) && m.exprs(x.Args, y.Args) &&
			m.noPos(x.Ellipsis, y.Ellipsis)
	case *ast.KeyValueExpr:
		y, ok := node.(*ast.KeyValueExpr)
		return ok && m.node(x.Key, y.Key) && m.node(x.Value, y.Value)
	case *ast.StarExpr:
		y, ok := node.(*ast.StarExpr)
		return ok && m.node(x.X, y.X)
	case *ast.SelectorExpr:
		y, ok := node.(*ast.SelectorExpr)
		return ok && m.node(x.X, y.X) && m.node(x.Sel, y.Sel)
	case *ast.IndexExpr:
		y, ok := node.(*ast.IndexExpr)
		return ok && m.node(x.X, y.X) && m.node(x.Index, y.Index)
	case *ast.SliceExpr:
		y, ok := node.(*ast.SliceExpr)
		return ok && m.node(x.X, y.X) && m.node(x.Low, y.Low) &&
			m.node(x.High, y.High) && m.node(x.Max, y.Max)
	case *ast.TypeAssertExpr:
		y, ok := node.(*ast.TypeAssertExpr)
		return ok && m.node(x.X, y.X) && m.node(x.Type, y.Type)

	// decls
	case *ast.GenDecl:
		y, ok := node.(*ast.GenDecl)
		return ok && x.Tok == y.Tok && m.specs(x.Specs, y.Specs)
	case *ast.FuncDecl:
		y, ok := node.(*ast.FuncDecl)
		return ok && m.fields(x.Recv, y.Recv) && m.node(x.Name, y.Name) &&
			m.node(x.Type, y.Type) && m.node(x.Body, y.Body)

	// specs
	case *ast.ValueSpec:
		y, ok := node.(*ast.ValueSpec)
		return ok && m.idents(x.Names, y.Names) &&
			m.node(x.Type, y.Type) && m.exprs(x.Values, y.Values)

	// stmt bridge nodes
	case *ast.ExprStmt:
		if id, ok := x.X.(*ast.Ident); ok && isWildName(id.Name) {
			// prefer matching $x as a statement, as it's
			// the parent
			return m.node(id, node)
		}
		y, ok := node.(*ast.ExprStmt)
		return ok && m.node(x.X, y.X)
	case *ast.DeclStmt:
		y, ok := node.(*ast.DeclStmt)
		return ok && m.node(x.Decl, y.Decl)

	// stmts
	case *ast.EmptyStmt:
		_, ok := node.(*ast.EmptyStmt)
		return ok
	case *ast.LabeledStmt:
		y, ok := node.(*ast.LabeledStmt)
		return ok && m.node(x.Label, y.Label) && m.node(x.Stmt, y.Stmt)
	case *ast.SendStmt:
		y, ok := node.(*ast.SendStmt)
		return ok && m.node(x.Chan, y.Chan) && m.node(x.Value, y.Value)
	case *ast.IncDecStmt:
		y, ok := node.(*ast.IncDecStmt)
		return ok && x.Tok == y.Tok && m.node(x.X, y.X)
	case *ast.AssignStmt:
		y, ok := node.(*ast.AssignStmt)
		return ok && x.Tok == y.Tok && m.exprs(x.Lhs, y.Lhs) &&
			m.exprs(x.Rhs, y.Rhs)
	case *ast.GoStmt:
		y, ok := node.(*ast.GoStmt)
		return ok && m.node(x.Call, y.Call)
	case *ast.DeferStmt:
		y, ok := node.(*ast.DeferStmt)
		return ok && m.node(x.Call, y.Call)
	case *ast.ReturnStmt:
		y, ok := node.(*ast.ReturnStmt)
		return ok && m.exprs(x.Results, y.Results)
	case *ast.BranchStmt:
		y, ok := node.(*ast.BranchStmt)
		return ok && x.Tok == y.Tok && m.node(maybeNilIdent(x.Label), maybeNilIdent(y.Label))
	case *ast.BlockStmt:
		y, ok := node.(*ast.BlockStmt)
		return ok && m.stmts(x.List, y.List)
	case *ast.IfStmt:
		y, ok := node.(*ast.IfStmt)
		return ok && m.node(x.Init, y.Init) && m.node(x.Cond, y.Cond) &&
			m.node(x.Body, y.Body) && m.node(x.Else, y.Else)
	case *ast.CaseClause:
		y, ok := node.(*ast.CaseClause)
		return ok && m.exprs(x.List, y.List) && m.stmts(x.Body, y.Body)
	case *ast.SwitchStmt:
		y, ok := node.(*ast.SwitchStmt)
		return ok && m.node(x.Init, y.Init) && m.node(x.Tag, y.Tag) && m.node(x.Body, y.Body)
	case *ast.TypeSwitchStmt:
		y, ok := node.(*ast.TypeSwitchStmt)
		return ok && m.node(x.Init, y.Init) && m.node(x.Assign, y.Assign) && m.node(x.Body, y.Body)
	case *ast.CommClause:
		y, ok := node.(*ast.CommClause)
		return ok && m.node(x.Comm, y.Comm) && m.stmts(x.Body, y.Body)
	case *ast.SelectStmt:
		y, ok := node.(*ast.SelectStmt)
		return ok && m.node(x.Body, y.Body)
	case *ast.ForStmt:
		y, ok := node.(*ast.ForStmt)
		return ok && m.node(x.Init, y.Init) && m.node(x.Cond, y.Cond) &&
			m.node(x.Post, y.Post) && m.node(x.Body, y.Body)
	case *ast.RangeStmt:
		y, ok := node.(*ast.RangeStmt)
		return ok && m.node(x.Key, y.Key) && m.node(x.Value, y.Value) &&
			m.node(x.X, y.X) && m.node(x.Body, y.Body)
	default:
		panic(fmt.Sprintf("unexpected node: %T", x))
	}
	panic(fmt.Sprintf("unfinished node: %T", expr))
}

func maybeNilIdent(x *ast.Ident) ast.Node {
	if x == nil {
		return nil
	}
	return x
}

func (m *matcher) noPos(p1, p2 token.Pos) bool {
	return (p1 == token.NoPos) == (p2 == token.NoPos)
}

type nodeList interface {
	at(i int) ast.Node
	len() int
	slice(from, to int) nodeList
	ast.Node
}

// nodes matches two lists of nodes. It uses a common algorithm to match
// wildcard patterns with any number of nodes without recursion.
func (m *matcher) nodes(ns1, ns2 nodeList) bool {
	// We need to keep a copy of m.values so that we can restart
	// with a different "any of" match while discarding any matches
	// we found while trying it.
	var oldMatches map[string]ast.Node
	backupMatches := func() {
		oldMatches = make(map[string]ast.Node, len(m.values))
		for k, v := range m.values {
			oldMatches[k] = v
		}
	}
	backupMatches()

	i1, i2 := 0, 0
	next1, next2 := 0, 0
	ns1len, ns2len := ns1.len(), ns2.len()
	wildName := ""
	wildStart := 0

	// wouldMatch returns whether the current wildcard - if any -
	// matches the nodes we are currently trying it on.
	wouldMatch := func() bool {
		switch wildName {
		case "", "_":
			return true
		}
		list := ns2.slice(wildStart, i2)
		// check that it matches any nodes found elsewhere
		prev, ok := m.values[wildName]
		if ok && !m.node(prev, list) {
			return false
		}
		m.values[wildName] = list
		return true
	}
	for i1 < ns1len || i2 < ns2len {
		if i1 < ns1len {
			n1 := ns1.at(i1)
			name, any := fromWildNode(n1)
			switch {
			case any:
				// keep track of where this wildcard
				// started (if name == wildName, we're
				// trying the same wildcard matching one
				// more node)
				if name != wildName {
					wildStart = i2
					wildName = name
				}
				// try to match zero or more at i2,
				// restarting at i2+1 if it fails
				next1 = i1
				next2 = i2 + 1
				i1++
				backupMatches()
				continue
			case i2 < ns2len && wouldMatch() && m.node(n1, ns2.at(i2)):
				wildName = ""
				// ordinary match
				i1++
				i2++
				continue
			}
		}
		// mismatch, try to restart
		if 0 < next2 && next2 <= ns2len {
			i1 = next1
			i2 = next2
			m.values = oldMatches
			continue
		}
		return false
	}
	if !wouldMatch() {
		return false
	}
	return true
}

func (m *matcher) exprs(exprs1, exprs2 []ast.Expr) bool {
	return m.nodes(exprList(exprs1), exprList(exprs2))
}

func (m *matcher) idents(ids1, ids2 []*ast.Ident) bool {
	return m.nodes(identList(ids1), identList(ids2))
}

func (m *matcher) stmts(stmts1, stmts2 []ast.Stmt) bool {
	return m.nodes(stmtList(stmts1), stmtList(stmts2))
}

func (m *matcher) specs(specs1, specs2 []ast.Spec) bool {
	return m.nodes(specList(specs1), specList(specs2))
}

func (m *matcher) fields(fields1, fields2 *ast.FieldList) bool {
	if fields1 == nil || fields2 == nil {
		return fields1 == fields2
	}
	if len(fields1.List) != len(fields2.List) {
		return false
	}
	for i, f1 := range fields1.List {
		if !m.node(f1, fields2.List[i]) {
			return false
		}
	}
	return true
}

// using a prefix is good enough for now
const (
	wildPrefix   = "_gogrep_"
	wildExtraAny = "any_"
)

func isWildName(name string) bool {
	return strings.HasPrefix(name, wildPrefix)
}

func fromWildName(s string) (name string, any bool) {
	s = strings.TrimPrefix(s, wildPrefix)
	name = strings.TrimPrefix(s, wildExtraAny)
	return name, name != s
}

func fromWildNode(node ast.Node) (name string, any bool) {
	switch x := node.(type) {
	case *ast.Ident:
		return fromWildName(x.Name)
	case *ast.ExprStmt:
		return fromWildNode(x.X)
	}
	return "", false
}

type exprList []ast.Expr
type identList []*ast.Ident
type stmtList []ast.Stmt
type specList []ast.Spec

func (l exprList) len() int  { return len(l) }
func (l identList) len() int { return len(l) }
func (l stmtList) len() int  { return len(l) }
func (l specList) len() int  { return len(l) }

func (l exprList) at(i int) ast.Node  { return l[i] }
func (l identList) at(i int) ast.Node { return l[i] }
func (l stmtList) at(i int) ast.Node  { return l[i] }
func (l specList) at(i int) ast.Node  { return l[i] }

func (l exprList) slice(i, j int) nodeList  { return l[i:j] }
func (l identList) slice(i, j int) nodeList { return l[i:j] }
func (l stmtList) slice(i, j int) nodeList  { return l[i:j] }
func (l specList) slice(i, j int) nodeList  { return l[i:j] }

func (l exprList) Pos() token.Pos  { return l[0].Pos() }
func (l identList) Pos() token.Pos { return l[0].Pos() }
func (l stmtList) Pos() token.Pos  { return l[0].Pos() }
func (l specList) Pos() token.Pos  { return l[0].Pos() }

func (l exprList) End() token.Pos  { return l[len(l)-1].End() }
func (l identList) End() token.Pos { return l[len(l)-1].End() }
func (l stmtList) End() token.Pos  { return l[len(l)-1].End() }
func (l specList) End() token.Pos  { return l[len(l)-1].End() }
