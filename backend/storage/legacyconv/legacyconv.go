// Package legacyconv converts the protobuf blobs a v0.0.615 cluster persisted
// under the previous api-contract (decoded with apigenold) into the shapes of
// the contract rewritten from the data model (apigen). Every function is pure:
// it takes a decoded old message or its bytes and returns the new message
// after Validate, or an error naming the old type and field when the value has
// no representation in the new model. Nothing is guessed; the caller's dry-run
// mode reports every refusal so the data or the rules can be fixed before the
// rollout.
//
// Rules the implementation plan leaves open, decided here:
//
//   - Sessions and Nix store resets had no integer id in their payload; the
//     log row's entity id (IDs.EntityID) becomes the new id.
//   - Keyslots take their fresh id from IDs.Keyslot, so the caller keeps one
//     mapping across every mutation of the same slot.
//   - Grants need the converted template to type their argument bindings, so
//     IDs.Template resolves a template id to the latest converted template.
//   - An old authz selector that matches nothing (unset, or an include list
//     emptied by its exclusions) is refused: the new validator rejects an empty
//     exact list, and a rule that never matched carried no access to keep.
//   - An entity-ref list is typed by the rule's entity-type position, which
//     must name exactly one kind. CLUSTER refs become SystemConfig targets;
//     ACCESS refs are refused because three target kinds share that position.
//   - A template argument's kind is the position that uses it; an argument no
//     rule uses is refused because its kind cannot be known.
//   - A listen selector with a family and no prefixes becomes the /0 prefix of
//     that family, which is how lib/ingressplan reads "every address of one
//     family" now; family ANY with no prefixes becomes an empty list.
//   - An OpendeploySpec payload becomes exactly internaldeploy.SelfSpec with
//     the release in the container version; its old networking section is
//     refused if it carried port forwards or ingress, and its mode is ignored.
//   - A set ConfigRef wins over the literal beside it in a setting (item 19);
//     the secret settings are present when the old ref named a secret.
//   - The old InternalUser.Delegated flag and SecretKeyslot.UpdatedAt have no
//     field in the new model and are dropped.
//
// Historic rows (IDs.Historic, a mutation that is not the entity's latest)
// get two repairs instead of refusals, each reported in Entity's second
// result: an env var that carries only an asset display key from before value
// references became pairs is dropped, and an empty system config
// network_ula_prefix takes IDs.UlaPrefix, the one prefix every revision of the
// immutable field shares. Strict mode never repairs.
package legacyconv

import (
	"fmt"
	"math"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

// TemplateLookup resolves a grant template id to the latest converted
// template, so a grant's argument bindings can be typed.
type TemplateLookup func(templateID uint64) (*apigen.AuthzGrantTemplate, bool)

// IDs carries what the old payloads do not: the log row's entity id for the
// kinds that had none in their payload, the fresh keyslot ids, the converted
// templates a grant refers to, and the historic-row mode with the ULA prefix
// its system config repair needs. A field is only consulted for the entity
// types that need it; Entity refuses a payload whose required field is nil.
type IDs struct {
	EntityID  uint64
	Keyslot   func(old apigenold.SecretKeyslot) uint64
	Template  TemplateLookup
	Historic  bool
	UlaPrefix []byte
}

// Entity decodes old as the previous CoreEntity and returns the new CoreEntity
// for type t with the repairs applied under historic mode, one line each. The
// old payload's set alternative must be the one t names.
func Entity(t apigen.CoreEntityType, old []byte, ids IDs) (*apigen.CoreEntity, []string, error) {
	e, err := apigenold.DecodeCoreEntity(old)
	if err != nil {
		return nil, nil, fmt.Errorf("CoreEntity(%v): decode: %w", t, err)
	}
	c := &conv{historic: ids.Historic, ulaPrefix: ids.UlaPrefix}
	var v apigen.CoreEntityValueOneof
	switch t {
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:
		v.Deployment, err = convertOrMissing(t, e.Deployment, func(old *apigenold.Deployment) (*apigen.Deployment, error) {
			return checked("Deployment", c.deployment(old), c.err)
		})
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
		v.ScheduledInstance, err = convertOrMissing(t, e.ScheduledInstance, ScheduledInstance)
	case apigen.CoreEntityType_CORE_ENTITY_NODE:
		v.Node, err = convertOrMissing(t, e.Node, Node)
	case apigen.CoreEntityType_CORE_ENTITY_SECRET:
		v.Secret, err = convertOrMissing(t, e.Secret, Secret)
	case apigen.CoreEntityType_CORE_ENTITY_CONFIG:
		v.Config, err = convertOrMissing(t, e.Config, Config)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET:
		v.Asset, err = convertOrMissing(t, e.Asset, Asset)
	case apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY:
		v.NetworkPolicy, err = convertOrMissing(t, e.NetworkPolicy, NetworkPolicy)
	case apigen.CoreEntityType_CORE_ENTITY_SPACE:
		v.Space, err = convertOrMissing(t, e.Space, Space)
	case apigen.CoreEntityType_CORE_ENTITY_USER:
		v.User, err = convertOrMissing(t, e.User, User)
	case apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY:
		v.ValueDirectory, err = convertOrMissing(t, e.ValueDirectory, ValueDirectory)
	case apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY:
		v.AssetDirectory, err = convertOrMissing(t, e.AssetDirectory, AssetDirectory)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE:
		v.AuthzGrantTemplate, err = convertOrMissing(t, e.AuthzRuleTemplate, AuthzGrantTemplate)
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT:
		if ids.Template == nil {
			return nil, nil, fmt.Errorf("CoreEntity(%v): IDs.Template is required to convert a grant", t)
		}
		v.AuthzGrant, err = convertOrMissing(t, e.AuthzGrant, func(old *apigenold.AuthzGrant) (*apigen.AuthzGrant, error) {
			return AuthzGrant(old, ids.Template)
		})
	case apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE:
		v.AuthzGlobalRule, err = convertOrMissing(t, e.AuthzGlobalRule, AuthzGlobalRule)
	case apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG:
		v.SystemConfig, err = convertOrMissing(t, e.SystemConfig, func(old *apigenold.SystemConfig) (*apigen.SystemConfig, error) {
			return checked("SystemConfig", c.systemConfig(old), c.err)
		})
	case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
		v.ScheduledInstanceStatus, err = convertOrMissing(t, e.ScheduledInstanceStatus, ScheduledInstanceStatus)
	case apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS:
		v.NodeStatus, err = convertOrMissing(t, e.NodeStatus, NodeStatus)
	case apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION:
		v.AgentSession, err = convertOrMissing(t, e.AgentSession, func(old *apigenold.AgentSession) (*apigen.AgentSession, error) {
			return AgentSession(old, ids.EntityID)
		})
	case apigen.CoreEntityType_CORE_ENTITY_USER_SESSION:
		v.UserSession, err = convertOrMissing(t, e.UserSession, func(old *apigenold.UserSession) (*apigen.UserSession, error) {
			return UserSession(old, ids.EntityID)
		})
	case apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET:
		v.NixStoreReset, err = convertOrMissing(t, e.NixStoreReset, func(old *apigenold.NixStoreReset) (*apigen.NixStoreReset, error) {
			return NixStoreReset(old, ids.EntityID)
		})
	case apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT:
		if ids.Keyslot == nil {
			return nil, nil, fmt.Errorf("CoreEntity(%v): IDs.Keyslot is required to convert a keyslot", t)
		}
		v.SecretKeyslot, err = convertOrMissing(t, e.SecretKeyslot, func(old *apigenold.SecretKeyslot) (*apigen.SecretKeyslot, error) {
			return SecretKeyslot(old, ids.Keyslot(*old))
		})
	default:
		return nil, nil, fmt.Errorf("CoreEntity: unsupported entity type %v", t)
	}
	if err != nil {
		return nil, nil, err
	}
	out := &apigen.CoreEntity{Value: v}
	if err := out.Validate(); err != nil {
		return nil, nil, fmt.Errorf("CoreEntity(%v): %w", t, err)
	}
	return out, c.repairs, nil
}

func convertOrMissing[O, N any](t apigen.CoreEntityType, old *O, convert func(*O) (*N, error)) (*N, error) {
	if old == nil {
		return nil, fmt.Errorf("CoreEntity(%v): the old payload does not carry that entity", t)
	}
	out, err := convert(old)
	if err != nil {
		return nil, fmt.Errorf("CoreEntity(%v): %w", t, err)
	}
	return out, nil
}

// conv accumulates the first refusal of one top-level conversion, and under
// historic mode the repairs applied instead, so the per-field helpers can stay
// expressions.
type conv struct {
	err       error
	historic  bool
	ulaPrefix []byte
	repairs   []string
}

func (c *conv) refuse(typ, field, format string, args ...any) {
	if c.err != nil {
		return
	}
	c.err = fmt.Errorf("%s", fieldMessage(typ, field, format, args...))
}

func (c *conv) repair(typ, field, format string, args ...any) {
	c.repairs = append(c.repairs, fieldMessage(typ, field, format, args...))
}

func fieldMessage(typ, field, format string, args ...any) string {
	if field == "" {
		return fmt.Sprintf("%s: %s", typ, fmt.Sprintf(format, args...))
	}
	return fmt.Sprintf("%s.%s: %s", typ, field, fmt.Sprintf(format, args...))
}

func (c *conv) id(typ, field string, v int64) uint64 {
	if v < 0 {
		c.refuse(typ, field, "negative id %d", v)
		return 0
	}
	return uint64(v)
}

func (c *conv) ids(typ, field string, vs []int32) []uint64 {
	if len(vs) == 0 {
		return nil
	}
	out := make([]uint64, len(vs))
	for i, v := range vs {
		out[i] = c.id(typ, field, int64(v))
	}
	return out
}

func (c *conv) optID(typ, field string, v int64) apigen.Maybe[uint64] {
	if v == 0 {
		return apigen.Maybe[uint64]{}
	}
	return apigen.Some(c.id(typ, field, v))
}

func (c *conv) optU32(typ, field string, v int32) apigen.Maybe[uint32] {
	if v == 0 {
		return apigen.Maybe[uint32]{}
	}
	return apigen.Some(c.u32(typ, field, int64(v)))
}

func (c *conv) optU64(typ, field string, v int64) apigen.Maybe[uint64] {
	if v == 0 {
		return apigen.Maybe[uint64]{}
	}
	if v < 0 {
		c.refuse(typ, field, "negative value %d", v)
		return apigen.Maybe[uint64]{}
	}
	return apigen.Some(uint64(v))
}

func (c *conv) u32(typ, field string, v int64) uint32 {
	if v < 0 || v > math.MaxUint32 {
		c.refuse(typ, field, "value %d does not fit uint32", v)
		return 0
	}
	return uint32(v)
}

func millis(v int64) apigen.Maybe[time.Time] {
	if v == 0 {
		return apigen.Maybe[time.Time]{}
	}
	return apigen.Some(time.UnixMilli(v))
}

func millisOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func optString(v string) apigen.Maybe[string] {
	if v == "" {
		return apigen.Maybe[string]{}
	}
	return apigen.Some(v)
}

func optBytes(v []byte) apigen.Maybe[[]byte] {
	if len(v) == 0 {
		return apigen.Maybe[[]byte]{}
	}
	return apigen.Some(v)
}

func checked[M interface{ Validate() error }](typ string, m M, err error) (M, error) {
	var zero M
	if err != nil {
		return zero, err
	}
	if err := m.Validate(); err != nil {
		return zero, fmt.Errorf("%s: %w", typ, err)
	}
	return m, nil
}

func (c *conv) secretRef(typ, field string, old *apigenold.ValueRef) apigen.SecretRef {
	if old == nil {
		c.refuse(typ, field, "unset reference")
		return apigen.SecretRef{}
	}
	return apigen.SecretRef{SecretID: c.id(typ, field, int64(old.ID)), Version: c.u32(typ, field, int64(old.Version))}
}

func (c *conv) configRef(typ, field string, old *apigenold.ValueRef) apigen.ConfigRef {
	if old == nil {
		c.refuse(typ, field, "unset reference")
		return apigen.ConfigRef{}
	}
	return apigen.ConfigRef{ConfigID: c.id(typ, field, int64(old.ID)), Version: c.u32(typ, field, int64(old.Version))}
}

func (c *conv) assetRef(typ, field string, old *apigenold.ValueRef) apigen.AssetRef {
	if old == nil {
		c.refuse(typ, field, "unset reference")
		return apigen.AssetRef{}
	}
	return apigen.AssetRef{AssetID: c.id(typ, field, int64(old.ID)), Version: c.u32(typ, field, int64(old.Version))}
}

func (c *conv) addr(typ, field, s string) apigen.IpAddress {
	a, err := apigen.ParseAddr(s)
	if err != nil {
		c.refuse(typ, field, "%q is not an IP address", s)
		return apigen.IpAddress{}
	}
	return a
}

func (c *conv) prefix(typ, field, s string) apigen.IpPrefix {
	p, err := apigen.ParsePrefix(s)
	if err != nil {
		c.refuse(typ, field, "%q is not an IP address or CIDR prefix", s)
		return apigen.IpPrefix{}
	}
	return p
}
