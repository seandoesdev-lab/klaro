// Package rbac holds pure authorization judgments: role hierarchy, the
// "minimum required role" check, and org-owner invariants. It is DB-agnostic
// (callers supply counts); no HTTP, no SQL (RBAC-01/02).
package rbac

// Role is an org membership role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// rank encodes the hierarchy owner > admin > member > viewer (RBAC-01).
var rank = map[Role]int{
	RoleViewer: 1,
	RoleMember: 2,
	RoleAdmin:  3,
	RoleOwner:  4,
}

// Rank returns the numeric rank of a role (0 for unknown).
func Rank(r Role) int { return rank[Role(string(r))] }

// Valid reports whether r is a recognized role.
func Valid(r Role) bool { _, ok := rank[r]; return ok }

// AtLeast reports whether have satisfies the minimum required role min.
// 상위 역할은 하위 권한을 포함한다. viewer 는 최하위(읽기 전용, D-5).
func AtLeast(have, min Role) bool {
	return Rank(have) >= Rank(min) && Rank(have) > 0
}
