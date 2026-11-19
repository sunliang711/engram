-- Remove implicit administrator access before restricting the allowed member roles.
ALTER TABLE tenant_members DROP CONSTRAINT tenant_members_role_check;
ALTER TABLE project_members DROP CONSTRAINT project_members_role_check;

-- Former tenant admins lose all grants in that tenant and must be explicitly reauthorized.
DELETE FROM project_members pm
USING projects p, tenant_members tm
WHERE pm.project_id = p.id AND tm.tenant_id = p.tenant_id
  AND tm.principal_id = pm.principal_id AND tm.role = 'admin';
UPDATE tenant_members SET role = 'none' WHERE role = 'admin';
UPDATE project_members SET role = 'writer' WHERE role = 'maintainer';

-- Console audit identities are never API members, including old installations.
DELETE FROM project_members WHERE principal_id IN (SELECT id FROM principals WHERE identity_provider = 'system_webui');
DELETE FROM tenant_members WHERE principal_id IN (SELECT id FROM principals WHERE identity_provider = 'system_webui');
UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, now())
WHERE principal_id IN (SELECT id FROM principals WHERE identity_provider = 'system_webui');

-- Cached write responses must not survive a change of authorization semantics.
DELETE FROM idempotency_requests;

ALTER TABLE tenant_members ADD CONSTRAINT tenant_members_role_check CHECK (role IN ('none', 'member', 'global_writer'));
ALTER TABLE project_members ADD CONSTRAINT project_members_role_check CHECK (role IN ('reader', 'writer'));
