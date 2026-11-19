-- Project creation is independent of global memory and existing project grants.
ALTER TABLE tenant_members ADD COLUMN can_create_project BOOLEAN NOT NULL DEFAULT FALSE;
