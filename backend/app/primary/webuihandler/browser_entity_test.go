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
	fillValue(reflect.ValueOf(&e.Value).Elem().Field(field), 0)
	return e
}

func expectedBrowserEntity(e *apigen.CoreEntity) *apigen.CoreEntity {
	if e.Value.Secret != nil {
		e.Value.Secret.Sealed = apigen.Maybe[apigen.SealedSecret]{}
	}
	if e.Value.User != nil {
		e.Value.User.Authentication = apigen.UserAuthentication{}
	}
	if e.Value.AgentSession != nil && e.Value.AgentSession.Token.Present {
		e.Value.AgentSession.Token.Value.Hash = apigen.Maybe[[]byte]{}
	}
	if e.Value.UserSession != nil {
		e.Value.UserSession.TokenHash = apigen.Maybe[[]byte]{}
	}
	if e.Value.SystemConfig != nil {
		e.Value.SystemConfig.MasterPasswordHash = apigen.Maybe[string]{}
	}
	return e
}

var sensitiveEntityFields = map[string]bool{"Secret": true, "User": true, "AgentSession": true, "UserSession": true, "SystemConfig": true}

func TestBrowserMutationStripsOnlySensitiveFieldsOfEveryEntity(t *testing.T) {
	oneofType := reflect.TypeOf(apigen.CoreEntityValueOneof{})
	if oneofType.NumField() == 0 {
		t.Fatal("CoreEntityValueOneof has no fields")
	}
	for i := 0; i < oneofType.NumField(); i++ {
		name := oneofType.Field(i).Name
		if reflect.DeepEqual(filledEntity(i), &apigen.CoreEntity{}) {
			t.Fatalf("%s: fill produced an empty entity", name)
		}
		want := expectedBrowserEntity(filledEntity(i))
		if reflect.DeepEqual(want, filledEntity(i)) != !sensitiveEntityFields[name] {
			t.Fatalf("%s: sensitive field expectation does not match the fill", name)
		}
		typ := apigen.CoreEntityType(i + 1)
		for _, tc := range []struct {
			kind  string
			input *apigen.CoreMutation
		}{
			{"create", &apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Create: &apigen.CreateMutation{EntityType: typ, EntityID: 42, Entity: *filledEntity(i)}}}},
			{"update", &apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Update: &apigen.UpdateMutation{EntityType: typ, EntityID: 42, Entity: *filledEntity(i)}}}},
		} {
			original := &apigen.CoreMutation{}
			if tc.input.Value.Create != nil {
				c := *tc.input.Value.Create
				c.Entity = *filledEntity(i)
				original.Value.Create = &c
			} else {
				u := *tc.input.Value.Update
				u.Entity = *filledEntity(i)
				original.Value.Update = &u
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
		del := &apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Delete: &apigen.DeleteMutation{EntityType: typ, EntityID: 42}}}
		if got := browserMutation(del); !reflect.DeepEqual(got, *del) {
			t.Fatalf("%s delete: browserMutation changed a delete: %+v", name, got)
		}
	}
}

func TestKeyslotsNeverReachTheBrowser(t *testing.T) {
	v := &streamVisibility{}
	keyslot := &apigen.CoreEntity{Value: apigen.CoreEntityValueOneof{SecretKeyslot: &apigen.SecretKeyslot{ID: 1, SmkVersion: 1}}}
	if v.entityVisible(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, 1, keyslot) {
		t.Fatal("secret keyslot was visible to an admin stream")
	}
}
