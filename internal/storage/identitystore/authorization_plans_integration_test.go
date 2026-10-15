//go:build integration

package identitystore

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationViewPlansAndResults(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	tx := integrationdb.BeginTx(t, ctx, pool)
	_, err := tx.Exec(ctx, `
INSERT INTO orgs(id,name,created_at,updated_at)
SELECT md5('org'||n)::uuid,'org'||n,now(),now() FROM generate_series(1,4000) n;
INSERT INTO projects(id,org_id,name,created_at,updated_at)
SELECT md5('project'||n)::uuid,md5('org'||n)::uuid,'project'||n,now(),now() FROM generate_series(1,4000) n;
INSERT INTO users(id,display_name,created_at,updated_at)
SELECT md5('user'||n)::uuid,'user'||n,now(),now() FROM generate_series(1,4000) n;
INSERT INTO org_memberships(org_id,user_id,role,created_at)
SELECT md5('org'||n)::uuid,md5('user'||n)::uuid,'member',now() FROM generate_series(1,4000) n;
INSERT INTO project_memberships(org_id,project_id,org_membership_id,role,created_at)
SELECT org_id,md5('project'||substring(o.name from 4))::uuid,m.id,'viewer',now()
FROM org_memberships m JOIN orgs o ON o.id=m.org_id;
UPDATE org_memberships SET role='owner' WHERE org_id=md5('org1')::uuid;
UPDATE org_memberships SET role='admin' WHERE org_id=md5('org2')::uuid;
INSERT INTO org_api_keys(id,org_id,name,token_id,token_hash,created_by_user_id,created_at,updated_at)
VALUES(md5('key')::uuid,md5('org3')::uuid,'key','key','key',md5('user3')::uuid,now(),now());
INSERT INTO org_memberships(org_id,org_api_key_id,role,created_at)
VALUES(md5('org3')::uuid,md5('key')::uuid,'member',now());
INSERT INTO project_memberships(org_id,project_id,org_membership_id,role,created_at)
SELECT org_id,md5('project3')::uuid,id,'developer',now() FROM org_memberships WHERE org_api_key_id IS NOT NULL;
UPDATE projects SET deleted_at=now() WHERE id=md5('project4')::uuid;
UPDATE orgs SET deleted_at=now() WHERE id=md5('org5')::uuid;
ANALYZE orgs; ANALYZE projects; ANALYZE org_memberships; ANALYZE project_memberships;
SET LOCAL plan_cache_mode=force_generic_plan;`)
	require.NoError(t, err)
	const oldView = `WITH active_projects AS (
SELECT project.org_id, project.id FROM projects project JOIN orgs org ON org.id=project.org_id
WHERE project.deleted_at IS NULL AND org.deleted_at IS NULL)
SELECT om.org_id,p.id AS project_id,om.user_id,om.org_api_key_id,'admin'::text AS role
FROM org_memberships om JOIN active_projects p ON p.org_id=om.org_id WHERE om.role IN ('owner','admin')
UNION
SELECT pm.org_id,pm.project_id,om.user_id,om.org_api_key_id,pm.role
FROM project_memberships pm JOIN org_memberships om ON om.org_id=pm.org_id AND om.id=pm.org_membership_id
JOIN active_projects p ON p.org_id=pm.org_id AND p.id=pm.project_id`
	var differences int
	require.NoError(t, tx.QueryRow(ctx, `WITH old_roles AS (`+oldView+`)
SELECT count(*) FROM ((SELECT * FROM old_roles EXCEPT SELECT * FROM principal_project_authorization_roles)
UNION ALL (SELECT * FROM principal_project_authorization_roles EXCEPT SELECT * FROM old_roles)) differences
`).Scan(&differences))
	require.Zero(t, differences)
	var org, project, user, key uuid.UUID
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT md5('org3')::uuid,md5('project3')::uuid,md5('user3')::uuid,md5('key')::uuid`,
	).Scan(&org, &project, &user, &key))
	for _, principal := range []string{"'" + user.String() + "',NULL", "NULL,'" + key.String() + "'"} {
		for i, test := range []struct{ name, args string }{
			{"ListPrincipalRoles", principal},
			{"ListProjectAuthorizationRolesForPrincipal", fmt.Sprintf("'%s','%s',%s", org, project, principal)},
			{"ListVisibleProjectRolesForPrincipal", fmt.Sprintf("%s,'%s',NULL,NULL,100", principal, org)},
		} {
			query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/identity.sql.go", test.name)
			name := fmt.Sprint("authorization", i)
			_, err := tx.Exec(ctx, "PREPARE "+name+" AS "+query)
			require.NoError(t, err)
			var raw json.RawMessage
			require.NoError(t, tx.QueryRow(ctx,
				"EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) EXECUTE "+name+"("+test.args+")",
			).Scan(&raw))
			t.Logf("%s %s: %s", test.name, principal, raw)
			integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
				if node.Relation == "orgs" || node.Relation == "projects" {
					require.False(t, strings.Contains(node.Type, "Seq Scan"), test.name)
				}
			})
			_, err = tx.Exec(ctx, "DEALLOCATE "+name)
			require.NoError(t, err)
		}
	}
}
