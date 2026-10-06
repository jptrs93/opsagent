package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var (
	ErrNotFound      = errors.New("authz: not found")
	ErrBuiltin       = errors.New("authz: builtin grant template is read-only")
	ErrNameTaken     = errors.New("authz: grant template name already in use")
	ErrTemplateInUse = errors.New("authz: grant template is referenced by grants")
	ErrInvalid       = errors.New("authz: invalid")
	ErrLastAdmin     = errors.New("authz: cannot delete the last access-managing grant")
)

var adminAccess = RequestedAccess{
	Verb:       apigen.AuthzVerb_AUTHZ_VERB_CREATE,
	SpaceID:    0,
	EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS,
}

type RequestedAccess struct {
	Verb       apigen.AuthzVerb
	SpaceID    uint64
	EntityType apigen.AuthzEntityKind
	EntityID   uint64
	Delegated  bool
}

type GrantTemplateRow struct {
	Name      string
	Author    int64
	CreatedAt int64
	Blob      []byte
}

type GrantRow struct {
	UserID    uint64
	Author    int64
	CreatedAt int64
	Blob      []byte
}

type GlobalRuleRow struct {
	Name      string
	Author    int64
	CreatedAt int64
	Blob      []byte
}

type Service struct {
	mu           sync.RWMutex
	store        *state.Service
	templates    map[uint64]*apigen.AuthzGrantTemplate
	grantsByUser map[uint64][]*apigen.AuthzGrant
	globalRules  []*apigen.AuthzGlobalRule
	now          func() time.Time
}

func Open(store *state.Service) (*Service, error) {
	s := &Service{
		store:        store,
		templates:    make(map[uint64]*apigen.AuthzGrantTemplate),
		grantsByUser: make(map[uint64][]*apigen.AuthzGrant),
		now:          time.Now,
	}
	ctx := context.Background()
	for _, b := range builtinTemplates() {
		if err := upsertBuiltinGrantTemplate(store, b.ID, b.Name, b.Spec.Encode()); err != nil {
			return nil, fmt.Errorf("authz: seed builtin %s: %w", b.Name, err)
		}
	}
	templateRows, err := store.Queries().ListAuthzGrantTemplates(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range templateRows {
		rec, err := pq.AuthzGrantTemplateEntity(row)
		if err != nil {
			return nil, fmt.Errorf("authz: decode grant template %d: %w", row.ID, err)
		}
		s.templates[rec.ID] = &rec
	}
	grantRows, err := store.Queries().ListAuthzGrants(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range grantRows {
		rec, err := pq.AuthzGrantEntity(row)
		if err != nil {
			return nil, fmt.Errorf("authz: decode grant %d: %w", row.ID, err)
		}
		s.grantsByUser[rec.UserID] = append(s.grantsByUser[rec.UserID], &rec)
	}
	for _, grants := range s.grantsByUser {
		sortByID(grants, func(g *apigen.AuthzGrant) uint64 { return g.ID })
	}
	if err := seedGlobalRule(store, DefaultUserVisibilityRuleName, defaultUserVisibilityRule().Encode()); err != nil {
		return nil, fmt.Errorf("authz: seed %s: %w", DefaultUserVisibilityRuleName, err)
	}
	globalRuleRows, err := store.Queries().ListAuthzGlobalRules(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range globalRuleRows {
		rec, err := pq.AuthzGlobalRuleEntity(row)
		if err != nil {
			return nil, fmt.Errorf("authz: decode global rule %d: %w", row.ID, err)
		}
		s.globalRules = append(s.globalRules, &rec)
	}
	sortByID(s.globalRules, func(r *apigen.AuthzGlobalRule) uint64 { return r.ID })
	return s, nil
}

func (s *Service) HasAccess(userID uint64, req RequestedAccess) bool {
	if req.Verb == apigen.AuthzVerb_AUTHZ_VERB_UNSPECIFIED || req.EntityType == apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_UNSPECIFIED || !SystemSpaceAllows(req) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Denies never apply to access management, so a rule that locks the
	// cluster out stays removable.
	if req.EntityType != apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_ACCESS {
		for _, r := range s.globalRules {
			if ruleMatches(&r.Rule, req, true) {
				return false
			}
		}
		for _, g := range s.grantsByUser[userID] {
			if s.grantMatchesLocked(g, req, true) {
				return false
			}
		}
	}
	for _, g := range s.grantsByUser[userID] {
		if s.grantMatchesLocked(g, req, false) {
			return true
		}
	}
	// Allow-mode global rules are grants everyone holds; denies above still
	// beat them.
	for _, r := range s.globalRules {
		if ruleMatches(&r.Rule, req, false) {
			return true
		}
	}
	return false
}

func (s *Service) SpaceVisible(userID uint64, spaceID uint64, delegated bool) bool {
	if s.HasAccess(userID, RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    spaceID,
		EntityType: apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SPACE,
		EntityID:   spaceID,
		Delegated:  delegated,
	}) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grantsByUser[userID] {
		if s.grantTouchesSpaceLocked(g, spaceID, delegated) {
			return true
		}
	}
	for _, r := range s.globalRules {
		if allowTouchesSpace(r.Rule.Effect, delegated) && spaceMatches(r.Rule.Selector.Spaces, spaceID) {
			return true
		}
	}
	return false
}

func (s *Service) CreateGrantTemplate(name string, spec *apigen.AuthzGrantTemplateSpec, author int64) (*apigen.AuthzGrantTemplate, error) {
	if err := validateTemplate(name, spec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.templateNameTakenLocked(name, 0) {
		return nil, ErrNameTaken
	}
	rec := &apigen.AuthzGrantTemplate{
		Name: name,
		Spec: cloneTemplateSpec(spec),
	}
	id, err := insertGrantTemplate(s.store, GrantTemplateRow{
		Name:      rec.Name,
		Author:    author,
		CreatedAt: s.now().UnixMilli(),
		Blob:      rec.Spec.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.templates[id] = rec
	return cloneTemplate(rec), nil
}

func (s *Service) UpdateGrantTemplate(id uint64, name string, spec *apigen.AuthzGrantTemplateSpec, author int64) (*apigen.AuthzGrantTemplate, error) {
	if err := validateTemplate(name, spec); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.templates[id]
	if existing == nil {
		return nil, ErrNotFound
	}
	if existing.Builtin {
		return nil, ErrBuiltin
	}
	if s.templateNameTakenLocked(name, id) {
		return nil, ErrNameTaken
	}
	rec := cloneTemplate(existing)
	rec.Name = name
	rec.Spec = cloneTemplateSpec(spec)
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			tg := g.Grant.Value.Template
			if tg == nil || tg.TemplateID != id {
				continue
			}
			if err := validateArgs(rec, tg.Args); err != nil {
				return nil, fmt.Errorf("authz: update would invalidate grant %d: %w", g.ID, err)
			}
		}
	}
	if err := updateGrantTemplate(s.store, id, name, rec.Spec.Encode(), author, s.now().UnixMilli()); err != nil {
		return nil, err
	}
	s.templates[id] = rec
	return cloneTemplate(rec), nil
}

func (s *Service) DeleteGrantTemplate(id uint64, author int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.templates[id]
	if existing == nil {
		return ErrNotFound
	}
	if existing.Builtin {
		return ErrBuiltin
	}
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			if tg := g.Grant.Value.Template; tg != nil && tg.TemplateID == id {
				return ErrTemplateInUse
			}
		}
	}
	if err := deleteGrantTemplate(s.store, id, author); err != nil {
		return err
	}
	delete(s.templates, id)
	return nil
}

func (s *Service) GrantTemplate(id uint64) (*apigen.AuthzGrantTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := s.templates[id]
	if rec == nil {
		return nil, ErrNotFound
	}
	return cloneTemplate(rec), nil
}

func (s *Service) GrantTemplates() []*apigen.AuthzGrantTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzGrantTemplate, 0, len(s.templates))
	for _, rec := range s.templates {
		out = append(out, cloneTemplate(rec))
	}
	sortByID(out, func(t *apigen.AuthzGrantTemplate) uint64 { return t.ID })
	return out
}

func (s *Service) templateNameTakenLocked(name string, excludeID uint64) bool {
	for _, rec := range s.templates {
		if rec.ID != excludeID && rec.Name == name {
			return true
		}
	}
	return false
}

func (s *Service) CreateGrant(g *apigen.AuthzGrant, author int64) (*apigen.AuthzGrant, error) {
	if g == nil || g.UserID == 0 {
		return nil, invalidf("authz: grant requires a user id")
	}
	if g.Grant.Value.Validate() != nil {
		return nil, invalidf("authz: grant must set exactly one of template and rule")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case g.Grant.Value.Rule != nil:
		if err := validateRule(g.Grant.Value.Rule); err != nil {
			return nil, fmt.Errorf("authz: grant rule: %w", err)
		}
	case g.Grant.Value.Template != nil:
		tg := g.Grant.Value.Template
		t := s.templates[tg.TemplateID]
		if t == nil {
			return nil, fmt.Errorf("authz: grant template %d: %w", tg.TemplateID, ErrNotFound)
		}
		if err := validateArgs(t, tg.Args); err != nil {
			return nil, err
		}
	}
	rec := cloneGrant(g)
	id, err := insertGrant(s.store, GrantRow{
		UserID:    rec.UserID,
		Author:    author,
		CreatedAt: s.now().UnixMilli(),
		Blob:      rec.Grant.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.grantsByUser[rec.UserID] = append(s.grantsByUser[rec.UserID], rec)
	return cloneGrant(rec), nil
}

func (s *Service) DeleteGrant(userID, id uint64, author int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	grants := s.grantsByUser[userID]
	idx := -1
	for i, g := range grants {
		if g.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	if s.grantMatchesLocked(grants[idx], adminAccess, false) && !s.otherAdminGrantExistsLocked(id) {
		return ErrLastAdmin
	}
	if err := deleteGrant(s.store, id, author); err != nil {
		return err
	}
	remaining := make([]*apigen.AuthzGrant, 0, len(grants)-1)
	remaining = append(remaining, grants[:idx]...)
	remaining = append(remaining, grants[idx+1:]...)
	if len(remaining) == 0 {
		delete(s.grantsByUser, userID)
	} else {
		s.grantsByUser[userID] = remaining
	}
	return nil
}

func (s *Service) Grant(userID, id uint64) (*apigen.AuthzGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grantsByUser[userID] {
		if g.ID == id {
			return cloneGrant(g), nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) GrantsForUser(userID uint64) []*apigen.AuthzGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grants := s.grantsByUser[userID]
	out := make([]*apigen.AuthzGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, cloneGrant(g))
	}
	return out
}

func (s *Service) Grants() []*apigen.AuthzGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzGrant, 0)
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			out = append(out, cloneGrant(g))
		}
	}
	sortByID(out, func(g *apigen.AuthzGrant) uint64 { return g.ID })
	return out
}

func (s *Service) CreateGlobalRule(name string, rule *apigen.AuthzRule, author int64) (*apigen.AuthzGlobalRule, error) {
	if err := validateGlobalRule(name, rule); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &apigen.AuthzGlobalRule{
		Name: name,
		Rule: cloneRule(rule),
	}
	id, err := insertGlobalRule(s.store, GlobalRuleRow{
		Name:      rec.Name,
		Author:    author,
		CreatedAt: s.now().UnixMilli(),
		Blob:      rec.Rule.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.globalRules = append(s.globalRules, rec)
	return cloneGlobalRule(rec), nil
}

func (s *Service) DeleteGlobalRule(id uint64, author int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, r := range s.globalRules {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	if err := deleteGlobalRule(s.store, id, author); err != nil {
		return err
	}
	remaining := make([]*apigen.AuthzGlobalRule, 0, len(s.globalRules)-1)
	remaining = append(remaining, s.globalRules[:idx]...)
	remaining = append(remaining, s.globalRules[idx+1:]...)
	s.globalRules = remaining
	return nil
}

func (s *Service) GlobalRule(id uint64) (*apigen.AuthzGlobalRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.globalRules {
		if r.ID == id {
			return cloneGlobalRule(r), nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) GlobalRules() []*apigen.AuthzGlobalRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzGlobalRule, 0, len(s.globalRules))
	for _, r := range s.globalRules {
		out = append(out, cloneGlobalRule(r))
	}
	return out
}

func cloneTemplate(rec *apigen.AuthzGrantTemplate) *apigen.AuthzGrantTemplate {
	c, err := apigen.DecodeAuthzGrantTemplate(rec.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone grant template: %v", err))
	}
	return c
}

func cloneTemplateSpec(t *apigen.AuthzGrantTemplateSpec) apigen.AuthzGrantTemplateSpec {
	c, err := apigen.DecodeAuthzGrantTemplateSpec(t.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone grant template spec: %v", err))
	}
	return *c
}

func cloneGrant(g *apigen.AuthzGrant) *apigen.AuthzGrant {
	c, err := apigen.DecodeAuthzGrant(g.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone grant: %v", err))
	}
	return c
}

func cloneRule(r *apigen.AuthzRule) apigen.AuthzRule {
	c, err := apigen.DecodeAuthzRule(r.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone rule: %v", err))
	}
	return *c
}

func cloneGlobalRule(rec *apigen.AuthzGlobalRule) *apigen.AuthzGlobalRule {
	c, err := apigen.DecodeAuthzGlobalRule(rec.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone global rule: %v", err))
	}
	return c
}

func sortByID[T any](items []T, id func(T) uint64) {
	sort.Slice(items, func(i, j int) bool { return id(items[i]) < id(items[j]) })
}
