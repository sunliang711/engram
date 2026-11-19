-- Agent names are optional self-reported metadata, independent of member authorization.
ALTER TABLE memories
    ADD COLUMN created_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN updated_agent TEXT NOT NULL DEFAULT '';
