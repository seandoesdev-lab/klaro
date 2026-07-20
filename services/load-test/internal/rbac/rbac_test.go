package rbac

import "testing"

func TestRankHierarchy(t *testing.T) {
	if !(Rank(RoleOwner) > Rank(RoleAdmin) &&
		Rank(RoleAdmin) > Rank(RoleMember) &&
		Rank(RoleMember) > Rank(RoleViewer)) {
		t.Fatal("owner>admin>member>viewer 서열이 깨졌다")
	}
	if Rank(Role("nonsense")) != 0 {
		t.Fatal("unknown role must rank 0")
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min Role
		want      bool
	}{
		{RoleOwner, RoleAdmin, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleMember, RoleAdmin, false},
		{RoleViewer, RoleMember, false},
		{RoleViewer, RoleViewer, true},
		{RoleMember, RoleViewer, true}, // 상위는 하위 포함
		{Role(""), RoleViewer, false},  // 미인식 역할 거부
	}
	for _, c := range cases {
		if got := AtLeast(c.have, c.min); got != c.want {
			t.Errorf("AtLeast(%q,%q)=%v want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestLastOwnerInvariants(t *testing.T) {
	// 마지막 owner 제거 차단
	if !WouldRemoveLastOwner(1, RoleOwner) {
		t.Error("1명 owner 제거는 차단되어야 함")
	}
	if WouldRemoveLastOwner(2, RoleOwner) {
		t.Error("owner 2명 중 1명 제거는 허용")
	}
	if WouldRemoveLastOwner(1, RoleMember) {
		t.Error("member 제거는 owner 불변식과 무관")
	}
	// 마지막 owner 강등 차단
	if !WouldDemoteLastOwner(1, RoleOwner, RoleAdmin) {
		t.Error("유일 owner 강등은 차단되어야 함")
	}
	if WouldDemoteLastOwner(1, RoleOwner, RoleOwner) {
		t.Error("owner→owner 는 강등이 아님")
	}
	if WouldDemoteLastOwner(2, RoleOwner, RoleMember) {
		t.Error("owner 2명 중 1명 강등은 허용")
	}
}
