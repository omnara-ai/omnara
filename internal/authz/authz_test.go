package authz

import "testing"

func TestOrgRoleAllows(t *testing.T) {
	tests := []struct {
		role    string
		action  string
		allowed bool
	}{
		{OrgRoleOwner, OrgRead, true},
		{OrgRoleOwner, OrgManage, true},
		{OrgRoleOwner, OrgOwn, true},
		{OrgRoleOwner, OrgSecretsList, true},
		{OrgRoleOwner, OrgSecretsManage, true},
		{OrgRoleAdmin, OrgRead, true},
		{OrgRoleAdmin, OrgManage, true},
		{OrgRoleAdmin, OrgOwn, false},
		{OrgRoleAdmin, OrgSecretsList, true},
		{OrgRoleAdmin, OrgSecretsManage, true},
		{OrgRoleMember, OrgRead, true},
		{OrgRoleMember, OrgManage, false},
		{OrgRoleMember, OrgOwn, false},
		{OrgRoleMember, OrgSecretsList, false},
		{OrgRoleMember, OrgSecretsManage, false},
		{"", OrgRead, false},
		{"", OrgManage, false},
		{"", OrgSecretsList, false},
		{"", OrgSecretsManage, false},
	}
	for _, tt := range tests {
		if got := OrgRoleAllows(tt.role, tt.action); got != tt.allowed {
			t.Fatalf("OrgRoleAllows(%q, %q)=%v want %v", tt.role, tt.action, got, tt.allowed)
		}
	}
}

func TestProjectRoleAllows(t *testing.T) {
	actions := []string{
		ProjectRead,
		ProjectManage,
		ProjectAccessManage,
		ProjectSecretsList,
		ProjectSecretsManage,
		AgentRead,
		AgentOperate,
	}
	allowedByRole := map[string]map[string]bool{
		ProjectRoleAdmin: {
			ProjectRead:          true,
			ProjectManage:        true,
			ProjectAccessManage:  true,
			ProjectSecretsList:   true,
			ProjectSecretsManage: true,
			AgentRead:            true,
			AgentOperate:         true,
		},
		ProjectRoleDeveloper: {
			ProjectRead:          true,
			ProjectManage:        true,
			ProjectSecretsList:   true,
			ProjectSecretsManage: true,
			AgentRead:            true,
			AgentOperate:         true,
		},
		ProjectRoleOperator: {
			ProjectRead: true, AgentRead: true, AgentOperate: true,
		},
		ProjectRoleViewer: {
			ProjectRead: true, AgentRead: true,
		},
	}
	for role, allowedActions := range allowedByRole {
		for _, action := range actions {
			want := allowedActions[action]
			if got := ProjectRoleAllows(role, action); got != want {
				t.Fatalf("ProjectRoleAllows(%q, %q)=%v want %v", role, action, got, want)
			}
		}
	}
	for _, action := range actions {
		if ProjectRoleAllows("", action) {
			t.Fatalf("empty project role should reject %s", action)
		}
	}
}

func TestActionImplies(t *testing.T) {
	tests := []struct {
		granted   string
		requested string
		allowed   bool
	}{
		{OrgOwn, OrgManage, true},
		{OrgOwn, OrgRead, true},
		{OrgManage, OrgOwn, false},
		{OrgManage, OrgSecretsManage, true},
		{OrgManage, OrgSecretsList, true},
		{OrgSecretsManage, OrgSecretsList, true},
		{OrgSecretsList, OrgSecretsManage, false},
		{ProjectManage, ProjectSecretsManage, true},
		{ProjectManage, ProjectSecretsList, true},
		{ProjectSecretsManage, ProjectSecretsList, true},
		{ProjectSecretsList, ProjectSecretsManage, false},
		{AgentOperate, AgentRead, true},
		{AgentRead, AgentOperate, false},
		{MachineManage, MachineRead, true},
		{MachineRead, MachineManage, false},
	}
	for _, tt := range tests {
		if got := ActionImplies(tt.granted, tt.requested); got != tt.allowed {
			t.Fatalf("ActionImplies(%q, %q)=%v want %v", tt.granted, tt.requested, got, tt.allowed)
		}
	}
}

func TestActionImpliesRejectsCycles(t *testing.T) {
	original := actionImplications
	actionImplications = map[string][]string{
		"cycle.a": {"cycle.b"},
		"cycle.b": {"cycle.a"},
	}
	defer func() { actionImplications = original }()

	if ActionImplies("cycle.a", "missing") {
		t.Fatal("cyclic implication graph should not imply missing actions")
	}
	if !ActionImplies("cycle.a", "cycle.b") {
		t.Fatal("direct implication inside cycle should still apply")
	}
}

func TestMachineRoleAllows(t *testing.T) {
	for _, action := range []string{MachineRead, MachineManage} {
		if !MachineRoleAllows(action) {
			t.Fatalf("machine predicate should allow machine action %s", action)
		}
	}
	if MachineRoleAllows(ProjectRead) {
		t.Fatal("machine predicate should reject non-machine action")
	}
}
