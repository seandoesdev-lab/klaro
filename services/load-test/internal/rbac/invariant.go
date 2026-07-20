package rbac

import "errors"

// ErrLastOwner is returned when an operation would leave an org with 0 owners
// (D-6: "org 당 owner ≥ 1" 불변식).
var ErrLastOwner = errors.New("org must retain at least one owner")

// WouldRemoveLastOwner reports whether removing a member with role targetRole
// from an org that currently has ownerCount owners would violate the invariant.
func WouldRemoveLastOwner(ownerCount int, targetRole Role) bool {
	return targetRole == RoleOwner && ownerCount <= 1
}

// WouldDemoteLastOwner reports whether changing a member from targetRole to
// newRole would drop the org below one owner.
func WouldDemoteLastOwner(ownerCount int, targetRole, newRole Role) bool {
	return targetRole == RoleOwner && newRole != RoleOwner && ownerCount <= 1
}
