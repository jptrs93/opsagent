package apigen

import "fmt"

// ValueRef is the kind-free form of SecretRef, ConfigRef, and AssetRef: the
// entity id and the value version. It is a cache key and a parameter type
// inside the backend, never a wire message; the field holding a typed ref
// says which entity kind it points at.
type ValueRef struct {
	ID      uint64
	Version uint32
}

// Valid reports whether r names a value: both the entity id and the value
// version are set.
func (r ValueRef) Valid() bool {
	return r.ID > 0 && r.Version > 0
}

func (r ValueRef) String() string {
	return fmt.Sprintf("%d@%d", r.ID, r.Version)
}

// Less orders refs by entity id, then value version.
func (r ValueRef) Less(o ValueRef) bool {
	return r.ID < o.ID || (r.ID == o.ID && r.Version < o.Version)
}

func (r SecretRef) Ref() ValueRef    { return ValueRef{ID: r.SecretID, Version: r.Version} }
func (r ConfigRef) Ref() ValueRef    { return ValueRef{ID: r.ConfigID, Version: r.Version} }
func (r AssetRef) Ref() ValueRef     { return ValueRef{ID: r.AssetID, Version: r.Version} }
func (r SecretRef) Valid() bool      { return r.Ref().Valid() }
func (r ConfigRef) Valid() bool      { return r.Ref().Valid() }
func (r AssetRef) Valid() bool       { return r.Ref().Valid() }
func (r SecretRef) String() string   { return r.Ref().String() }
func (r ConfigRef) String() string   { return r.Ref().String() }
func (r AssetRef) String() string    { return r.Ref().String() }
func (r ValueRef) Secret() SecretRef { return SecretRef{SecretID: r.ID, Version: r.Version} }
func (r ValueRef) Config() ConfigRef { return ConfigRef{ConfigID: r.ID, Version: r.Version} }
func (r ValueRef) Asset() AssetRef   { return AssetRef{AssetID: r.ID, Version: r.Version} }

func SecretRefs(refs []ValueRef) []SecretRef {
	out := make([]SecretRef, len(refs))
	for i, r := range refs {
		out[i] = r.Secret()
	}
	return out
}

func ConfigRefs(refs []ValueRef) []ConfigRef {
	out := make([]ConfigRef, len(refs))
	for i, r := range refs {
		out[i] = r.Config()
	}
	return out
}
