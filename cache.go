package csvutil

import (
	"reflect"
	"sort"
	"sync"
)

var fieldCache sync.Map // map[typeKey]*fieldPlan

type fieldPlan struct {
	fields    fields
	conflicts []FieldConflict
}

func cachedFields(k typeKey) (fields, error) {
	if v, ok := fieldCache.Load(k); ok {
		plan := v.(*fieldPlan)
		return plan.fields, fieldConflictError(plan.conflicts)
	}

	v, _ := fieldCache.LoadOrStore(k, buildFields(k))
	plan := v.(*fieldPlan)
	return plan.fields, fieldConflictError(plan.conflicts)
}

type field struct {
	name     string
	baseType reflect.Type
	typ      reflect.Type
	tag      tag
	index    []int
	path     string
}

type fields []field

func (fs fields) Len() int { return len(fs) }

func (fs fields) Swap(i, j int) { fs[i], fs[j] = fs[j], fs[i] }

func (fs fields) Less(i, j int) bool {
	shortest := len(fs[i].index)
	if len(fs[j].index) < shortest {
		shortest = len(fs[j].index)
	}
	for k := 0; k < shortest; k++ {
		if fs[i].index[k] != fs[j].index[k] {
			return fs[i].index[k] < fs[j].index[k]
		}
	}
	return len(fs[i].index) < len(fs[j].index)
}

type typeKey struct {
	tag string
	typ reflect.Type
}

type fieldMap map[string]fields

func (m fieldMap) insert(f field) {
	m[f.name] = append(m[f.name], f)
}

func (m fieldMap) plan() *fieldPlan {
	out := make(fields, 0, len(m))
	conflicts := make([]FieldConflict, 0)

	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		candidates := m[name]
		explicit := make(fields, 0, len(candidates))
		implicit := make(fields, 0, len(candidates))
		for _, f := range candidates {
			if f.tag.empty {
				implicit = append(implicit, f)
			} else {
				explicit = append(explicit, f)
			}
		}

		chosen := implicit
		if len(explicit) > 0 {
			chosen = explicit
		}

		shortestDepth := len(chosen[0].index)
		for _, f := range chosen[1:] {
			if len(f.index) < shortestDepth {
				shortestDepth = len(f.index)
			}
		}

		shortest := make(fields, 0, len(chosen))
		for _, f := range chosen {
			if len(f.index) == shortestDepth {
				shortest = append(shortest, f)
			}
		}
		sort.Sort(shortest)

		if len(shortest) > 1 {
			paths := make([]string, len(shortest))
			for i, f := range shortest {
				paths[i] = f.path
			}
			conflicts = append(conflicts, FieldConflict{
				Column: name,
				Paths:  paths,
			})
			continue
		}

		out = append(out, shortest[0])
	}

	sort.Sort(out)
	return &fieldPlan{fields: out, conflicts: conflicts}
}

func buildFields(k typeKey) *fieldPlan {
	type key struct {
		reflect.Type
		tag
	}

	q := fields{{typ: k.typ}}
	visited := make(map[key]struct{})
	fm := make(fieldMap)

	for len(q) > 0 {
		f := q[0]
		q = q[1:]

		key := key{f.typ, f.tag}
		if _, ok := visited[key]; ok {
			continue
		}
		visited[key] = struct{}{}

		depth := len(f.index)

		numField := f.typ.NumField()
		for i := 0; i < numField; i++ {
			sf := f.typ.Field(i)

			if sf.PkgPath != "" && !sf.Anonymous {
				// unexported field
				continue
			}

			if sf.Anonymous {
				t := sf.Type
				if t.Kind() == reflect.Ptr {
					t = t.Elem()
				}
				if sf.PkgPath != "" && t.Kind() != reflect.Struct {
					// ignore embedded unexported non-struct fields.
					continue
				}
			}

			tag := parseTag(k.tag, sf)
			if tag.ignore {
				continue
			}
			if f.tag.prefix != "" {
				tag.prefix = f.tag.prefix + tag.prefix
			}

			ft := sf.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}

			newf := field{
				name:     tag.prefix + tag.name,
				baseType: sf.Type,
				typ:      ft,
				tag:      tag,
				index:    makeIndex(f.index, i),
				path:     joinFieldPath(f.path, sf.Name),
			}

			if sf.Anonymous && ft.Kind() == reflect.Struct && tag.empty {
				q = append(q, newf)
				continue
			}

			if tag.inline && ft.Kind() == reflect.Struct {
				q = append(q, newf)
				continue
			}

			fm.insert(newf)

			// look for duplicate nodes on the same level. Nodes won't be
			// revisited, so write all fields for the current type now.
			for _, v := range q {
				if len(v.index) != depth {
					break
				}
				if v.typ == f.typ && v.tag.prefix == tag.prefix {
					// other nodes can have different path.
					fm.insert(field{
						name:     tag.prefix + tag.name,
						baseType: sf.Type,
						typ:      ft,
						tag:      tag,
						index:    makeIndex(v.index, i),
						path:     joinFieldPath(v.path, sf.Name),
					})
				}
			}
		}
	}
	return fm.plan()
}

func makeIndex(index []int, v int) []int {
	out := make([]int, len(index), len(index)+1)
	copy(out, index)
	return append(out, v)
}

func joinFieldPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func fieldConflictError(conflicts []FieldConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	return &FieldConflictError{Conflicts: conflicts}
}
