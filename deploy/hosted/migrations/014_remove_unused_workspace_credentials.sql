-- Hosted owner APIs authorize verified workspace membership directly. They
-- never use the recoverable scope credential originally reserved for them.
-- Keep only the opaque scope digest required by the common runtime schema.
ALTER TABLE hosted_workspaces DROP COLUMN owner_credential;
