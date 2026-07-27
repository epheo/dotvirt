package model

import (
	"reflect"
	"testing"
)

// Empty hand-enumerates VMEdit's fields. A field added without extending it
// silently classifies that edit as "nothing to do" (StageEdit rejects it, Adopt
// finds no drift), so pin: a VMEdit with ANY field set must not be Empty.
func TestVMEditEmptyCoversEveryField(t *testing.T) {
	typ := reflect.TypeOf(VMEdit{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		e := reflect.New(typ).Elem()
		fv := e.Field(i)
		switch f.Type.Kind() {
		case reflect.Pointer:
			fv.Set(reflect.New(f.Type.Elem()))
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(f.Type, 1, 1))
		case reflect.Map:
			m := reflect.MakeMap(f.Type)
			m.SetMapIndex(reflect.New(f.Type.Key()).Elem(), reflect.New(f.Type.Elem()).Elem())
			fv.Set(m)
		default:
			t.Fatalf("field %s has kind %s; teach this test how to set it", f.Name, f.Type.Kind())
		}
		if e.Interface().(VMEdit).Empty() {
			t.Errorf("VMEdit with only %s set reads as Empty; extend Empty()", f.Name)
		}
	}
}
