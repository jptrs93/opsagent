package metricstore

import (
	"reflect"

	"github.com/parquet-go/parquet-go"
)

type table[S any, R any] struct {
	name    string
	decode  func([]byte) (*S, error)
	encode  func(*S) []byte
	timeOf  func(*S) int64
	toRow   func(*S) R
	fromRow func(*R) *S
	sorting []parquet.SortingColumn
}

type rowFieldPair struct {
	row, sample int
	optional    bool
}

type rowMapper[S any, R any] struct {
	pairs []rowFieldPair
}

func newRowMapper[S any, R any]() rowMapper[S, R] {
	var m rowMapper[S, R]
	rt := reflect.TypeFor[R]()
	st := reflect.TypeFor[S]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		sf, ok := st.FieldByName(f.Name)
		if !ok {
			continue
		}
		switch {
		case sf.Type == f.Type:
			m.pairs = append(m.pairs, rowFieldPair{row: i, sample: sf.Index[0]})
		case f.Type.Kind() == reflect.Pointer && isMaybeOf(sf.Type, f.Type.Elem()):
			m.pairs = append(m.pairs, rowFieldPair{row: i, sample: sf.Index[0], optional: true})
		}
	}
	return m
}

func isMaybeOf(t, elem reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() != 2 {
		return false
	}
	v, ok := t.FieldByName("Value")
	if !ok || v.Type != elem {
		return false
	}
	p, ok := t.FieldByName("Present")
	return ok && p.Type.Kind() == reflect.Bool
}

func (m rowMapper[S, R]) toRow(s *S) R {
	var r R
	rv := reflect.ValueOf(&r).Elem()
	sv := reflect.ValueOf(s).Elem()
	for _, p := range m.pairs {
		dst, src := rv.Field(p.row), sv.Field(p.sample)
		if !p.optional {
			dst.Set(src)
			continue
		}
		if src.FieldByName("Present").Bool() {
			ptr := reflect.New(dst.Type().Elem())
			ptr.Elem().Set(src.FieldByName("Value"))
			dst.Set(ptr)
		}
	}
	return r
}

func (m rowMapper[S, R]) fromRow(r *R) *S {
	s := new(S)
	rv := reflect.ValueOf(r).Elem()
	sv := reflect.ValueOf(s).Elem()
	for _, p := range m.pairs {
		dst, src := sv.Field(p.sample), rv.Field(p.row)
		if !p.optional {
			dst.Set(src)
			continue
		}
		if !src.IsNil() {
			dst.FieldByName("Value").Set(src.Elem())
			dst.FieldByName("Present").SetBool(true)
		}
	}
	return s
}
