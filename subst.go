// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
)

func (m *matcher) cmdSubst(cmd exprCmd, subs []submatch) []submatch {
	for _, sub := range subs {
		nodeCopy, _ := m.parseExpr(cmd.src)
		// since we'll want to set positions within the file's
		// FileSet
		scrubPositions(nodeCopy)

		m.fillParents(nodeCopy)
		m.fillValues(nodeCopy, sub.values)
		m.substNode(sub.node, nodeCopy)
		sub.node = nodeCopy
	}
	return subs
}

func (m *matcher) fillValues(node ast.Node, values map[string]ast.Node) {
	inspect(node, func(node ast.Node) bool {
		id := fromWildNode(node)
		info := m.info(id)
		if info.name == "" {
			return true
		}
		prev := values[info.name]
		switch prev.(type) {
		case exprList:
			node = exprList([]ast.Expr{node.(*ast.Ident)})
		}
		m.substNode(node, prev)
		return true
	})
}

func (m *matcher) substNode(oldNode, newNode ast.Node) {
	parent := m.parentOf(oldNode)
	m.setParentOf(newNode, parent)

	ptr := m.nodePtr(oldNode)
	switch x := ptr.(type) {
	case **ast.Ident:
		*x = newNode.(*ast.Ident)
	case *ast.Expr:
		*x = newNode.(ast.Expr)
	case *ast.Stmt:
		switch y := newNode.(type) {
		case ast.Expr:
			stmt := &ast.ExprStmt{X: y}
			m.setParentOf(stmt, parent)
			*x = stmt
		case ast.Stmt:
			*x = y
		default:
			panic(fmt.Sprintf("cannot replace stmt with %T", y))
		}
	case *[]ast.Expr:
		oldList := oldNode.(exprList)
		var first, last []ast.Expr
		for i, expr := range *x {
			if expr == oldList[0] {
				first = (*x)[:i]
				last = (*x)[i+len(oldList):]
				break
			}
		}
		switch y := newNode.(type) {
		case ast.Expr:
			*x = append(first, y)
		case exprList:
			*x = append(first, y...)
		default:
			panic(fmt.Sprintf("cannot replace exprs with %T", y))
		}
		*x = append(*x, last...)
	case *[]ast.Stmt:
		oldList := oldNode.(stmtList)
		var first, last []ast.Stmt
		for i, stmt := range *x {
			if stmt == oldList[0] {
				first = (*x)[:i]
				last = (*x)[i+len(oldList):]
				break
			}
		}
		switch y := newNode.(type) {
		case ast.Expr:
			stmt := &ast.ExprStmt{X: y}
			m.setParentOf(stmt, parent)
			*x = append(first, stmt)
		case ast.Stmt:
			*x = append(first, y)
		case stmtList:
			*x = append(first, y...)
		default:
			panic(fmt.Sprintf("cannot replace stmts with %T", y))
		}
		*x = append(*x, last...)
	default:
		panic(fmt.Sprintf("unsupported substitution: %T", x))
	}
	// the new nodes have scrubbed positions, so try our best to use
	// sensible ones
	fixPositions(parent)
}

func (m *matcher) parentOf(node ast.Node) ast.Node {
	list, ok := node.(nodeList)
	if ok {
		node = list.at(0)
	}
	return m.parents[node]
}

func (m *matcher) setParentOf(node, parent ast.Node) {
	list, ok := node.(nodeList)
	if ok {
		if list.len() == 0 {
			return
		}
		node = list.at(0)
	}
	m.parents[node] = parent
}

func (m *matcher) nodePtr(node ast.Node) interface{} {
	list, wantSlice := node.(nodeList)
	if wantSlice {
		node = list.at(0)
	}
	parent := m.parentOf(node)
	if parent == nil {
		return nil
	}
	v := reflect.ValueOf(parent).Elem()
	for i := 0; i < v.NumField(); i++ {
		fld := v.Field(i)
		switch fld.Type().Kind() {
		case reflect.Slice:
			for i := 0; i < fld.Len(); i++ {
				ifld := fld.Index(i)
				if ifld.Interface() != node {
					continue
				}
				if wantSlice {
					return fld.Addr().Interface()
				}
				return ifld.Addr().Interface()
			}
		case reflect.Interface:
			if fld.Interface() == node {
				return fld.Addr().Interface()
			}
		}
	}
	return nil
}

// nodePosHash is an ast.Node that can always be used as a key in maps,
// even for nodes that are slices like nodeList.
type nodePosHash struct {
	pos, end token.Pos
}

func (n nodePosHash) Pos() token.Pos { return n.pos }
func (n nodePosHash) End() token.Pos { return n.end }

func posHash(node ast.Node) nodePosHash {
	return nodePosHash{pos: node.Pos(), end: node.End()}
}

var posType = reflect.TypeOf(token.NoPos)

func scrubPositions(node ast.Node) {
	inspect(node, func(node ast.Node) bool {
		v := reflect.ValueOf(node)
		if v.Kind() != reflect.Ptr {
			return true
		}
		v = v.Elem()
		if v.Kind() != reflect.Struct {
			return true
		}
		for i := 0; i < v.NumField(); i++ {
			fld := v.Field(i)
			if fld.Type() == posType {
				fld.SetInt(0)
			}
		}
		return true
	})
}

func fixPositions(node ast.Node) {
	fallback := func(pos *token.Pos, to token.Pos) {
		if !pos.IsValid() {
			*pos = to
		}
	}
	ast.Inspect(node, func(node ast.Node) bool {
		// TODO: many more node types
		switch x := node.(type) {
		case *ast.GoStmt:
			fallback(&x.Go, x.Call.Pos())
		}
		return true
	})
}
