package authz

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
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

type ChangeKind int

const (
	ChangeRuleTemplates ChangeKind = iota + 1
	ChangeGrants
	ChangeGlobalRules
	// ChangeVisibilityInputs marks a change to state outside the authz tables
	// that feeds visibility decisions (node allow lists), so subscribed streams
	// re-filter what each user sees.
	ChangeVisibilityInputs
)

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
	Deleted   bool
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

type Store interface {
	ListAuthzRuleTemplates() ([]RuleTemplateRow, error)
	InsertAuthzRuleTemplate(row RuleTemplateRow) (int64, error)
	UpdateAuthzRuleTemplate(id int64, name string, blob []byte, author, updatedAt int64) error
	DeleteAuthzRuleTemplate(id int64) error
	UpsertAuthzRuleTemplate(id int64, name string, blob []byte) error
	ListAuthzGrants() ([]GrantRow, error)
	InsertAuthzGrant(row GrantRow) (int64, error)
	DeleteAuthzGrant(id int64) error
	ListAuthzGlobalRules() ([]GlobalRuleRow, error)
	InsertAuthzGlobalRule(row GlobalRuleRow) (int64, error)
	DeleteAuthzGlobalRule(id int64) error
	SeedAuthzGlobalRule(name string, blob []byte) error
}

type Service struct {
	mu           sync.RWMutex
	store        Store
	templates    map[int64]*apigen.AuthzRuleTemplateRecord
	grantsByUser map[int64][]*apigen.AuthzGrantRecord
	globalRules  []*apigen.AuthzGlobalRuleRecord
	now          func() time.Time
	subs         *pubsubu.PubSub[ChangeKind]
}

func Open(store Store) (*Service, error) {
	s := &Service{
		store:        store,
		templates:    make(map[int64]*apigen.AuthzRuleTemplateRecord),
		grantsByUser: make(map[int64][]*apigen.AuthzGrantRecord),
		now:          time.Now,
		subs:         &pubsubu.PubSub[ChangeKind]{},
	}
	for _, b := range builtinTemplates() {
		if err := store.UpsertAuthzRuleTemplate(b.ID, b.Name, b.Template.Encode()); err != nil {
			return nil, fmt.Errorf("authz: seed builtin %s: %w", b.Name, err)
		}
	}
	templateRows, err := store.ListAuthzRuleTemplates()
	if err != nil {
		return nil, err
	}
	for _, row := range templateRows {
		content, err := apigen.DecodeAuthzRuleTemplate(row.Blob)
		if err != nil {
			return nil, fmt.Errorf("authz: decode rule template %d: %w", row.ID, err)
		}
		s.templates[row.ID] = &apigen.AuthzRuleTemplateRecord{
			ID:        row.ID,
			Name:      row.Name,
			Builtin:   row.Builtin,
			Deleted:   row.Deleted,
			Author:    row.Author,
			CreatedAt: row.CreatedAt,
			Template:  content,
		}
	}
	grantRows, err := store.ListAuthzGrants()
	if err != nil {
		return nil, err
	}
	for _, row := range grantRows {
		content, err := apigen.DecodeAuthzGrant(row.Blob)
		if err != nil {
			return nil, fmt.Errorf("authz: decode grant %d: %w", row.ID, err)
		}
		rec := &apigen.AuthzGrantRecord{
			ID:         row.ID,
			UserID:     row.UserID,
			TemplateID: row.TemplateID,
			Author:     row.Author,
			CreatedAt:  row.CreatedAt,
			Grant:      content,
		}
		s.grantsByUser[rec.UserID] = append(s.grantsByUser[rec.UserID], rec)
	}
	for _, grants := range s.grantsByUser {
		sortByID(grants, func(g *apigen.AuthzGrantRecord) int64 { return g.ID })
	}
	if err := store.SeedAuthzGlobalRule(DefaultUserVisibilityRuleName, defaultUserVisibilityRule().Encode()); err != nil {
		return nil, fmt.Errorf("authz: seed %s: %w", DefaultUserVisibilityRuleName, err)
	}
	globalRuleRows, err := store.ListAuthzGlobalRules()
	if err != nil {
		return nil, err
	}
	for _, row := range globalRuleRows {
		content, err := apigen.DecodeAuthzGlobalRule(row.Blob)
		if err != nil {
			return nil, fmt.Errorf("authz: decode global rule %d: %w", row.ID, err)
		}
		s.globalRules = append(s.globalRules, &apigen.AuthzGlobalRuleRecord{
			ID:        row.ID,
			Name:      row.Name,
			Author:    row.Author,
			CreatedAt: row.CreatedAt,
			Rule:      content,
		})
	}
	sortByID(s.globalRules, func(r *apigen.AuthzGlobalRuleRecord) int64 { return r.ID })
	return s, nil
}

func (s *Service) SubscribeChanges() (*pubsubu.Sub[ChangeKind], func()) {
	sub := s.subs.Subscribe(nil)
	return sub, sub.Unsubscribe
}

func (s *Service) NotifyVisibilityInputsChanged() {
	s.subs.Notify(ChangeVisibilityInputs)
}

func (s *Service) HasAccess(userID int64, req RequestedAccess) bool {
	if req.Verb == apigen.AuthzVerb_AUTHZ_VERB_UNKNOWN || req.EntityType == apigen.AuthzEntity_AUTHZ_ENTITY_UNKNOWN {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if req.EntityType != apigen.AuthzEntity_AUTHZ_ENTITY_ACCESS {
		for _, r := range s.globalRules {
			if globalDenyMatches(r.Rule, req) {
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
		if globalAllowMatches(r.Rule, req) {
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
		if r.Rule == nil || r.Rule.Deny || (delegated && !r.Rule.DelegationAllowed) {
			continue
		}
		if selectorMatches(r.Rule.Spaces, nil, spaceID) {
			return true
		}
	}
	return false
}

func (s *Service) CreateRuleTemplate(name string, template *apigen.AuthzRuleTemplate, author int64) (*apigen.AuthzRuleTemplateRecord, error) {
	if err := validateTemplate(name, template); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.templateNameTakenLocked(name, 0) {
		return nil, ErrNameTaken
	}
	rec := &apigen.AuthzRuleTemplateRecord{
		Name:      name,
		Author:    author,
		CreatedAt: s.now().UnixMilli(),
		Template:  cloneTemplateContent(template),
	}
	id, err := s.store.InsertAuthzRuleTemplate(RuleTemplateRow{
		Name:      rec.Name,
		Author:    rec.Author,
		CreatedAt: rec.CreatedAt,
		Blob:      rec.Template.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.templates[id] = rec
	s.subs.Notify(ChangeRuleTemplates)
	return cloneTemplateRecord(rec), nil
}

func (s *Service) UpdateRuleTemplate(id int64, name string, template *apigen.AuthzRuleTemplate, author int64) (*apigen.AuthzRuleTemplateRecord, error) {
	if err := validateTemplate(name, template); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.templates[id]
	if existing == nil || existing.Deleted {
		return nil, ErrNotFound
	}
	if existing.Builtin {
		return nil, ErrBuiltin
	}
	if s.templateNameTakenLocked(name, id) {
		return nil, ErrNameTaken
	}
	rec := cloneTemplateRecord(existing)
	rec.Name = name
	rec.Author = author
	rec.Template = cloneTemplateContent(template)
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			if g.TemplateID != id {
				continue
			}
			var bindings []*apigen.AuthzArgumentBinding
			if g.Grant != nil {
				bindings = g.Grant.Args
			}
			if err := validateArgs(rec, bindings); err != nil {
				return nil, fmt.Errorf("authz: update would invalidate grant %d: %w", g.ID, err)
			}
		}
	}
	if err := s.store.UpdateAuthzRuleTemplate(id, name, rec.Template.Encode(), author, s.now().UnixMilli()); err != nil {
		return nil, err
	}
	s.templates[id] = rec
	s.subs.Notify(ChangeRuleTemplates)
	return cloneTemplateRecord(rec), nil
}

func (s *Service) DeleteRuleTemplate(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.templates[id]
	if existing == nil || existing.Deleted {
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
	rec := cloneTemplateRecord(existing)
	rec.Deleted = true
	if err := s.store.DeleteAuthzRuleTemplate(id); err != nil {
		return err
	}
	s.templates[id] = rec
	s.subs.Notify(ChangeRuleTemplates)
	return nil
}

func (s *Service) RuleTemplate(id int64) (*apigen.AuthzRuleTemplateRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := s.templates[id]
	if rec == nil || rec.Deleted {
		return nil, ErrNotFound
	}
	return cloneTemplateRecord(rec), nil
}

func (s *Service) RuleTemplates() []*apigen.AuthzRuleTemplateRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzRuleTemplateRecord, 0, len(s.templates))
	for _, rec := range s.templates {
		if rec.Deleted {
			continue
		}
		out = append(out, cloneTemplateRecord(rec))
	}
	sortByID(out, func(t *apigen.AuthzRuleTemplateRecord) int64 { return t.ID })
	return out
}

func (s *Service) templateNameTakenLocked(name string, excludeID int64) bool {
	for _, rec := range s.templates {
		if !rec.Deleted && rec.ID != excludeID && rec.Name == name {
			return true
		}
	}
	return false
}

func (s *Service) CreateGrant(g *apigen.AuthzGrantRecord) (*apigen.AuthzGrantRecord, error) {
	if g == nil || g.UserID <= 0 {
		return nil, invalidf("authz: grant requires a user id")
	}
	content := g.Grant
	if content == nil {
		content = &apigen.AuthzGrant{}
	}
	if (g.TemplateID != 0) == (content.Rule != nil) {
		return nil, invalidf("authz: grant must set exactly one of template_id and rule")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := cloneGrantRecord(g)
	if rec.Grant == nil {
		rec.Grant = &apigen.AuthzGrant{}
	}
	if rec.Grant.Rule != nil {
		if len(rec.Grant.Args) != 0 {
			return nil, invalidf("authz: args are only valid with a template")
		}
		if err := validateRules([]*apigen.AuthzRule{rec.Grant.Rule}, false); err != nil {
			return nil, err
		}
	} else {
		t := s.templates[rec.TemplateID]
		if t == nil || t.Deleted {
			return nil, fmt.Errorf("authz: rule template %d: %w", rec.TemplateID, ErrNotFound)
		}
		if err := validateArgs(t, rec.Grant.Args); err != nil {
			return nil, err
		}
	}
	rec.CreatedAt = s.now().UnixMilli()
	id, err := s.store.InsertAuthzGrant(GrantRow{
		UserID:     rec.UserID,
		TemplateID: rec.TemplateID,
		Author:     rec.Author,
		CreatedAt:  rec.CreatedAt,
		Blob:       rec.Grant.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.grantsByUser[rec.UserID] = append(s.grantsByUser[rec.UserID], rec)
	s.subs.Notify(ChangeGrants)
	return cloneGrantRecord(rec), nil
}

func (s *Service) DeleteGrant(userID, id int64) error {
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
	if err := s.store.DeleteAuthzGrant(id); err != nil {
		return err
	}
	remaining := make([]*apigen.AuthzGrantRecord, 0, len(grants)-1)
	remaining = append(remaining, grants[:idx]...)
	remaining = append(remaining, grants[idx+1:]...)
	if len(remaining) == 0 {
		delete(s.grantsByUser, userID)
	} else {
		s.grantsByUser[userID] = remaining
	}
	s.subs.Notify(ChangeGrants)
	return nil
}

func (s *Service) Grant(userID, id int64) (*apigen.AuthzGrantRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grantsByUser[userID] {
		if g.ID == id {
			return cloneGrantRecord(g), nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) GrantsForUser(userID int64) []*apigen.AuthzGrantRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grants := s.grantsByUser[userID]
	out := make([]*apigen.AuthzGrantRecord, 0, len(grants))
	for _, g := range grants {
		out = append(out, cloneGrantRecord(g))
	}
	return out
}

func (s *Service) Grants() []*apigen.AuthzGrantRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzGrantRecord, 0)
	for _, grants := range s.grantsByUser {
		for _, g := range grants {
			out = append(out, cloneGrantRecord(g))
		}
	}
	sortByID(out, func(g *apigen.AuthzGrantRecord) int64 { return g.ID })
	return out
}

func (s *Service) CreateGlobalRule(name string, rule *apigen.AuthzGlobalRule, author int64) (*apigen.AuthzGlobalRuleRecord, error) {
	if err := validateGlobalRule(name, rule); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &apigen.AuthzGlobalRuleRecord{
		Name:      name,
		Author:    author,
		CreatedAt: s.now().UnixMilli(),
		Rule:      cloneGlobalRule(rule),
	}
	id, err := s.store.InsertAuthzGlobalRule(GlobalRuleRow{
		Name:      rec.Name,
		Author:    rec.Author,
		CreatedAt: rec.CreatedAt,
		Blob:      rec.Rule.Encode(),
	})
	if err != nil {
		return nil, err
	}
	rec.ID = id
	s.globalRules = append(s.globalRules, rec)
	s.subs.Notify(ChangeGlobalRules)
	return cloneGlobalRuleRecord(rec), nil
}

func (s *Service) DeleteGlobalRule(id int64) error {
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
	if err := s.store.DeleteAuthzGlobalRule(id); err != nil {
		return err
	}
	remaining := make([]*apigen.AuthzGlobalRuleRecord, 0, len(s.globalRules)-1)
	remaining = append(remaining, s.globalRules[:idx]...)
	remaining = append(remaining, s.globalRules[idx+1:]...)
	s.globalRules = remaining
	s.subs.Notify(ChangeGlobalRules)
	return nil
}

func (s *Service) GlobalRule(id int64) (*apigen.AuthzGlobalRuleRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.globalRules {
		if r.ID == id {
			return cloneGlobalRuleRecord(r), nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) GlobalRules() []*apigen.AuthzGlobalRuleRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apigen.AuthzGlobalRuleRecord, 0, len(s.globalRules))
	for _, r := range s.globalRules {
		out = append(out, cloneGlobalRuleRecord(r))
	}
	return out
}

func cloneTemplateRecord(rec *apigen.AuthzRuleTemplateRecord) *apigen.AuthzRuleTemplateRecord {
	c, err := apigen.DecodeAuthzRuleTemplateRecord(rec.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone rule template record: %v", err))
	}
	return c
}

func cloneTemplateContent(t *apigen.AuthzRuleTemplate) *apigen.AuthzRuleTemplate {
	c, err := apigen.DecodeAuthzRuleTemplate(t.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone rule template content: %v", err))
	}
	return c
}

func cloneGrantRecord(g *apigen.AuthzGrantRecord) *apigen.AuthzGrantRecord {
	c, err := apigen.DecodeAuthzGrantRecord(g.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone grant record: %v", err))
	}
	return c
}

func cloneGlobalRule(r *apigen.AuthzGlobalRule) *apigen.AuthzGlobalRule {
	c, err := apigen.DecodeAuthzGlobalRule(r.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone global rule: %v", err))
	}
	return c
}

func cloneGlobalRuleRecord(rec *apigen.AuthzGlobalRuleRecord) *apigen.AuthzGlobalRuleRecord {
	c, err := apigen.DecodeAuthzGlobalRuleRecord(rec.Encode())
	if err != nil {
		panic(fmt.Sprintf("authz: clone global rule record: %v", err))
	}
	return c
}

func sortByID[T any](items []T, id func(T) int64) {
	sort.Slice(items, func(i, j int) bool { return id(items[i]) < id(items[j]) })
}
