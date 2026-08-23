package tenancy

import "strings"

// Role is a caller's role within an org.
//
// The four roles are the klaro authorization model (design section 2.1:
// "Org > Project > Resource. 역할: owner / admin / member / viewer"). The
// observability plane is org-scoped throughout - no route carries a
// :projectId - so the org role is the whole decision here; the project tier of
// the hierarchy is where S1 job resources sit.
type Role string

// The klaro roles, ordered least to most privileged by rank below.
const (
	RoleViewer Role = "viewer"
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// ranks orders the roles. A map rather than iota constants so an unknown role
// has no rank at all: a typo in a token claim must not land next to "viewer"
// and pick up read access by accident.
var ranks = map[Role]int{
	RoleViewer: 1,
	RoleMember: 2,
	RoleAdmin:  3,
	RoleOwner:  4,
}

// ParseRole normalises a role claim, reporting whether it is one of the four.
//
// Case is folded because an identity provider that emits "Admin" is a
// configuration detail, not a different role. Anything else is rejected rather
// than downgraded: a caller whose role we cannot name has no rank.
func ParseRole(s string) (Role, bool) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	_, ok := ranks[r]
	return r, ok
}

// Valid reports whether r is one of the four klaro roles.
func (r Role) Valid() bool {
	_, ok := ranks[r]
	return ok
}

// AtLeast reports whether r carries at least min's privilege.
//
// An unknown role is never at least anything, so a guard built on this fails
// closed on a role it does not recognise. An unknown *requirement* also refuses
// everything, so a typo in the route table shows up as a 403 on the first
// request rather than as an endpoint with no gate.
func (r Role) AtLeast(min Role) bool {
	have, ok := ranks[r]
	if !ok {
		return false
	}
	want, ok := ranks[min]
	if !ok {
		return false
	}
	return have >= want
}

// Principal is the authenticated caller: which org, with what role.
//
// Subject is carried for audit and logging only. Nothing authorizes on it, so a
// token with an odd sub cannot widen access.
type Principal struct {
	OrgID   string
	Role    Role
	Subject string
}
