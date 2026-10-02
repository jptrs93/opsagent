package authz

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var (
	ErrNotFound      = errors.New("authz: not found")
	ErrBuiltin       = errors.New("authz: builtin rule template is read-only")
	ErrNameTaken     = errors.New("authz: rule template name already in use")
	ErrTemplateInUse = errors.New("authz: rule template is referenced by grants")
	ErrInvalid       = errors.New("authz: invalid")
	ErrLastAdmin     = errors.New("authz: cannot delete the last access-managing grant")
)

var adminAccess = RequestedAccess{
	Verb:       apigen.AuthzVerb_AUTHZ_VERB_CREATE,
	SpaceID:    0,
	EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS,
}

type RequestedAccess struct {
	Verb       apigen.AuthzVerb
	SpaceID    int64
	EntityType apigen.AuthzEntity
	EntityID   int64
	Delegated  bool
}

type RuleTemplateRow struct {
	ID        int64
	Name      string
	Builtin   bool
	Author    int64
	CreatedAt int64
	Blob      []byte
}

type GrantRow struct {
	ID         int64
	UserID     int64
	TemplateID int64
	Author     int64
	CreatedAt  int64
	Blob       []byte
}

type GlobalRuleRow struct {
	ID        int64
	Name      string
	Author    int64
	CreatedAt int64
	Blob      []byte
}

type Service struct {
	mu           sync.RWMutex
	store        *state.Service
	templates    map[int64]*apigen.AuthzRuleTemplate
	grantsByUser map[int64][]*apigen.AuthzGrant
	globalRules  []*apigen.AuthzGlobalRule
	now          func() time.Time
}

func Open(store *state.Service) (*Service, error) {
	s := &Service{
		store:        store,
		templates:    make(map[int64]*apigen.AuthzRuleTemplate),
		grantsByUser: make(map[int64][]*apigen.AuthzGrant),
		now:          time.Now,
	}
	for _, b := range builtinTemplates() {
		if err := upsertBuiltinRuleTemplate(store, b.ID, b.Name, b.Spec.Encode()); err != nil {
			return nil, fmt.Errorf("authz: seed builtin %s: %w", b.Name, err)
		}
	}
	templateRows, err := listRuleTemplates(store.Queries())
	if err != nil {
		return nil, err
	}
	for _, row := range templateRows {
		content, err := apigen.DecodeAuthzRuleTemplateSpec(row.Blob)
		if err != nil {
			return nil, fmt.Errorf("authz: decode rule template %d: %w", row.ID, err)
		}
		s.templates[row.ID] = &apigen.AuthzRuleTemplate{
			ID:      row.ID,
			Name:    row.Name,
			Builtin: row.Builtin,
			Spec:    content,
		}
	}
	grantRows, err := listGrants(store.Queries())
	if err != nil {
		return nil, err
	}
	for _, row := range grantRows {
		content, err := apigen.DecodeAuthzGrantSpec(row.Blob)
		if err != nil {
			return nil, fmt.Errorf("authz: decode grant %d: %w", row.ID, err)
		}
		rec := &apigen.AuthzGrant{
			ID:         row.ID,
			UserID:     row.UserID,
			TemplateID: row.TemplateID,
			Spec:       content,
		}
		s.grantsByUser[rec.UserID] = append(s.grantsByUser[rec.UserID], rec)
	}
	for _, grants := range s.grantsByUser {
		sortByID(grants, func(g *apigen.AuthzGrant) int64 { return g.ID })
	}
	if err := seedGlobalRule(store, DefaultUserVisibilityRuleName, defaultUserVisibilityRule().Encode()); err != nil {
		return nil, fmt.Errorf("authz: seed %s: %w", DefaultUserVisibilityRuleName, err)
	}
	globalRuleRows, err := listGlobalRules(store.Queries())
	if err != nil {
		return nil, err
	}
	for _, row := range globalRuleRows {
		content, err := apigen.DecodeAuthzGlobalRuleSpec(row.Blob)
		if err != nil {
			return nil, fmt.Errorf("authz: decode global rule %d: %w", row.ID, err)
		}
		s.globalRules = append(s.globalRules, &apigen.AuthzGlobalRule{
			ID:   row.ID,
			Name: row.Name,
			Spec: content,
		})
	}
	sortByID(s.globalRules, func(r *apigen.AuthzGlobalRule) int64 { return r.ID })
	return s, nil
}

func (s *Service) HasAccess(userID int64, req RequestedAccess) bool {
	if req.Verb == apigen.AuthzVerb_AUTHZ_VERB_UNKNOWN || req.EntityType == apigen.AuthzEntity_AUTHZ_ENTITY_UNKNOWN || !SystemSpaceAllows(req) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if req.EntityType != apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS {
		for _, r := range s.globalRules {
			if globalDenyMatches(r.Spec, req) {
				return false
			}
		}
	}
	for _, g := range s.grantsByUser[userID] {
		if s.grantMatchesLocked(g, req) {
			return true
		}
	}
	// Allow-mode global rules are grants everyone holds; denies above still
	// beat them.
	for _, r := range s.globalRules {
		if globalAllowMatches(r.Spec, req) {
			return true
		}
	}
	return false
}

func (s *Service) SpaceVisible(userID int64, spaceID int64, delegated bool) bool {
	if s.HasAccess(userID, RequestedAccess{
		Verb:       apigen.AuthzVerb_AUTHZ_VERB_VIEW,
		SpaceID:    spaceID,
		EntityType: apigen.AuthzEntity_AUTHZ_ENTITY_SPACE,
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
		if r.Spec == nil || r.Spec.Deny || (delegated && !r.Spec.DelegationAllowed) {
			continue
		}
		if selectorMatches(r.Spec.Spaces, nil, spaceID) {
			return true
		}
	}
	return false
}

func (s *Service) CreateRuleTemplate(name string, template *apigen.AuthzRuleTemplateSpec, author int64) (*apigen.AuthzRuleTemplate, error) {
	if err := validateTemplate(name, template); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.templateNameTakenLocked(name, 0) {
		return nil, ErrNameTaken
	}
	rec := &apigen.AuthzRuleTemplate{
		Name: name,
		Spec: cloneTemplateSpec(template),
	}
	id, err := insertRuleTemplate(s.store, RuleTemplateRow{
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

func (s *Service) UpdateRuleTemplate(id int64, name string, template *apigen.AuthzRuleTemplateSpec, author int64) (*apigen.AuthzRuleTemplate, error) {
	if err := validateTemplate(name, template); err != nil {
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
	rec.Spec = cloneTemplateSpec(template)
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			if g.TemplateID != id {
				continue
			}
			var bindings []*apigen.AuthzArgumentBinding
			if g.Spec != nil {
				bindings = g.Spec.Args
			}
			if err := validateArgs(rec, bindings); err != nil {
				return nil, fmt.Errorf("authz: update would invalidate grant %d: %w", g.ID, err)
			}
		}
	}
	if err := updateRuleTemplate(s.store, id, name, rec.Spec.Encode(), author, s.now().UnixMilli()); err != nil {
		return nil, err
	}
	s.templates[id] = rec
	return cloneTemplate(rec), nil
}

func (s *Service) DeleteRuleTemplate(id, author int64) error {
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
			if g.TemplateID == id {
				return ErrTemplateInUse
			}
		}
	}
	if err := deleteRuleTemplate(s.store, id, author); err != nil {
		return err
	}
	delete(s.templates, id)
	return nil
}

func (s *Service) RuleTemplate(id int64) (*apigen.AuthzRuleTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := s.templates[id]
	if rec == nil {
		return nil, ErrNotFound
	}
	return cloneTemplate(rec), nil
}

func (s *Service) RuleTemplates() []*apigen.AuthzRuleTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzRuleTemplate, 0, len(s.templates))
	for _, rec := range s.templates {
		out = append(out, cloneTemplate(rec))
	}
	sortByID(out, func(t *apigen.AuthzRuleTemplate) int64 { return t.ID })
	return out
}

func (s *Service) templateNameTakenLocked(name string, excludeID int64) bool {
	for _, rec := range s.templates {
		if rec.ID != excludeID && rec.Name == name {
			return true
		}
	}
	return false
}

func (s *Service) CreateGrant(g *apigen.AuthzGrant, author int64) (*apigen.AuthzGrant, error) {
	if g == nil || g.UserID <= 0 {
		return nil, invalidf("authz: grant requires a user id")
	}
	content := g.Spec
	if content == nil {
		content = &apigen.AuthzGrantSpec{}
	}
	if (g.TemplateID != 0) == (content.Rule != nil) {
		return nil, invalidf("authz: grant must set exactly one of template_id and rule")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := cloneGrant(g)
	if rec.Spec == nil {
		rec.Spec = &apigen.AuthzGrantSpec{}
	}
	if rec.Spec.Rule != nil {
		if len(rec.Spec.Args) != 0 {
			return nil, invalidf("authz: args are only valid with a template")
		}
		if err := validateRules([]*apigen.AuthzRule{rec.Spec.Rule}, false); err != nil {
			return nil, err
		}
	} else {
		t := s.templates[rec.TemplateID]
		if t == nil {
			return nil, fmt.Errorf("authz: rule template %d: %w", rec.TemplateID, ErrNotFound)
		}
		if err := validateArgs(t, rec.Spec.Args); err != nil {
			return nil, err
		}
	}
	id, err := insertGrant(s.store, GrantRow{
		UserID:     rec.UserID,
		TemplateID: rec.TemplateID,
		Author:     author,
		CreatedAt:  s.now().UnixMilli(),
		Blob:       rec.Spec.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.grantsByUser[rec.UserID] = append(s.grantsByUser[rec.UserID], rec)
	return cloneGrant(rec), nil
}

func (s *Service) DeleteGrant(userID, id, author int64) error {
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
	if s.grantMatchesLocked(grants[idx], adminAccess) && !s.otherAdminGrantExistsLocked(id) {
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

func (s *Service) Grant(userID, id int64) (*apigen.AuthzGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grantsByUser[userID] {
		if g.ID == id {
			return cloneGrant(g), nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) GrantsForUser(userID int64) []*apigen.AuthzGrant {
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
	sortByID(out, func(g *apigen.AuthzGrant) int64 { return g.ID })
	return out
}

func (s *Service) CreateGlobalRule(name string, rule *apigen.AuthzGlobalRuleSpec, author int64) (*apigen.AuthzGlobalRule, error) {
	if err := validateGlobalRule(name, rule); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &apigen.AuthzGlobalRule{
		Name: name,
		Spec: cloneGlobalRuleSpec(rule),
	}
	id, err := insertGlobalRule(s.store, GlobalRuleRow{
		Name:      rec.Name,
		Author:    author,
		CreatedAt: s.now().UnixMilli(),
		Blob:      rec.Spec.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.globalRules = append(s.globalRules, rec)
	return cloneGlobalRule(rec), nil
}

func (s *Service) DeleteGlobalRule(id, author int64) error {
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

func (s *Service) GlobalRule(id int64) (*apigen.AuthzGlobalRule, error) {
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

func cloneTemplate(rec *apigen.AuthzRuleTemplate) *apigen.AuthzRuleTemplate {
	c, err := apigen.DecodeAuthzRuleTemplate(rec.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone rule template: %v", err))
	}
	return c
}

func cloneTemplateSpec(t *apigen.AuthzRuleTemplateSpec) *apigen.AuthzRuleTemplateSpec {
	c, err := apigen.DecodeAuthzRuleTemplateSpec(t.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone rule template spec: %v", err))
	}
	return c
}

func cloneGrant(g *apigen.AuthzGrant) *apigen.AuthzGrant {
	c, err := apigen.DecodeAuthzGrant(g.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone grant: %v", err))
	}
	return c
}

func cloneGlobalRuleSpec(r *apigen.AuthzGlobalRuleSpec) *apigen.AuthzGlobalRuleSpec {
	c, err := apigen.DecodeAuthzGlobalRuleSpec(r.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone global rule spec: %v", err))
	}
	return c
}

func cloneGlobalRule(rec *apigen.AuthzGlobalRule) *apigen.AuthzGlobalRule {
	c, err := apigen.DecodeAuthzGlobalRule(rec.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone global rule: %v", err))
	}
	return c
}

func sortByID[T any](items []T, id func(T) int64) {
	sort.Slice(items, func(i, j int) bool { return id(items[i]) < id(items[j]) })
}
