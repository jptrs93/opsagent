package webuihandler

import (
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func fillValue(v reflect.Value, depth int) {
	if depth > 8 {
		return
	}
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillValue(v.Elem(), depth+1)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Unix(1700000000, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillValue(v.Field(i), depth+1)
			}
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte{7, 8, 9})
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillValue(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillValue(k, depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		fillValue(e, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.String:
		v.SetString("filled")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	}
}

func filledEntity(field int) *apigen.CoreEntity {
	e := &apigen.CoreEntity{}
	fillValue(reflect.ValueOf(e).Elem().Field(field), 0)
	return e
}

func expectedBrowserEntity(e *apigen.CoreEntity) *apigen.CoreEntity {
	if e.Secret != nil {
		e.Secret.SmkVersion, e.Secret.Ciphertext, e.Secret.Nonce = 0, nil, nil
	}
	if e.User != nil {
		e.User.Credentials = nil
	}
	if e.AgentSession != nil {
		e.AgentSession.TokenHash = nil
	}
	if e.UserSession != nil {
		e.UserSession.TokenHash = nil
	}
	if e.SystemConfig != nil {
		e.SystemConfig.MasterPasswordHash = ""
	}
	e.SecretKeyslot = nil
	return e
}

func TestBrowserMutationStripsOnlySensitiveFieldsOfEveryEntity(t *testing.T) {
	entityType := reflect.TypeOf(apigen.CoreEntity{})
	if entityType.NumField() == 0 {
		t.Fatal("CoreEntity has no fields")
	}
	for i := 0; i < entityType.NumField(); i++ {
		name := entityType.Field(i).Name
		if reflect.DeepEqual(filledEntity(i), &apigen.CoreEntity{}) {
			t.Fatalf("%s: fill produced an empty entity", name)
		}
		want := expectedBrowserEntity(filledEntity(i))
		if reflect.DeepEqual(want, filledEntity(i)) != (name != "Secret" && name != "User" && name != "AgentSession" && name != "UserSession" && name != "SystemConfig" && name != "SecretKeyslot") {
			t.Fatalf("%s: sensitive field expectation does not match the fill", name)
		}
		typ := apigen.CoreEntityType(i + 1)
		for _, tc := range []struct {
			kind  string
			input *apigen.CoreMutation
		}{
			{"create", &apigen.CoreMutation{Create: &apigen.CreateMutation{EntityType: typ, EntityID: 42, Entity: filledEntity(i)}}},
			{"update", &apigen.CoreMutation{Update: &apigen.UpdateMutation{EntityType: typ, EntityID: 42, Entity: filledEntity(i)}}},
		} {
			original := &apigen.CoreMutation{}
			if tc.input.Create != nil {
				c := *tc.input.Create
				c.Entity = filledEntity(i)
				original.Create = &c
			} else {
				u := *tc.input.Update
				u.Entity = filledEntity(i)
				original.Update = &u
			}
			got := browserMutation(tc.input)
			if !reflect.DeepEqual(tc.input, original) {
				t.Fatalf("%s %s: browserMutation modified its input", name, tc.kind)
			}
			if got.Kind() != tc.input.Kind() || got.Type() != typ || got.EntityID() != 42 {
				t.Fatalf("%s %s: envelope changed: %+v", name, tc.kind, got)
			}
			if !reflect.DeepEqual(got.Entity(), want) {
				t.Fatalf("%s %s: browser entity = %+v, want %+v", name, tc.kind, got.Entity(), want)
			}
		}
		del := &apigen.CoreMutation{Delete: &apigen.DeleteMutation{EntityType: typ, EntityID: 42}}
		if got := browserMutation(del); !reflect.DeepEqual(got, del) {
			t.Fatalf("%s delete: browserMutation changed a delete: %+v", name, got)
		}
	}
}
