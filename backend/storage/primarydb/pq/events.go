package pq

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	EventCreate = int64(apigen.AuthzVerb_AUTHZ_VERB_CREATE)
	EventUpdate = int64(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	EventDelete = int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
)
