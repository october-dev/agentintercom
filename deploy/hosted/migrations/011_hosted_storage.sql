-- Logical retained-data accounting. File bytes have their separate allowance.
ALTER TABLE hosted_workspaces ADD COLUMN paused_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE hosted_workspaces ADD COLUMN text_limit BIGINT NOT NULL DEFAULT 5368709120 CHECK(text_limit>0);
ALTER TABLE hosted_workspaces ADD COLUMN maintenance_attempted_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE hosted_workspaces ADD COLUMN maintenance_completed_at BIGINT NOT NULL DEFAULT 0;
CREATE TABLE hosted_storage_usage (
 scope_id TEXT COLLATE "C" PRIMARY KEY REFERENCES hosted_workspaces(scope_id) ON DELETE CASCADE,
 retained_bytes BIGINT NOT NULL DEFAULT 0 CHECK(retained_bytes>=0)
);

-- Persist the parent's scope on child rows so cascading deletes can account for
-- them even after the parent disappears. Existing runtime INSERTs remain valid.
ALTER TABLE output_values ADD COLUMN scope_id TEXT COLLATE "C";
UPDATE output_values v SET scope_id=s.scope_id FROM output_streams s WHERE s.stream_id=v.stream_id;
ALTER TABLE output_values ALTER COLUMN scope_id SET NOT NULL;
ALTER TABLE output_values ADD FOREIGN KEY(scope_id) REFERENCES scopes(scope_id) ON DELETE CASCADE;
ALTER TABLE a2a_message_correlations ADD COLUMN scope_id TEXT COLLATE "C";
UPDATE a2a_message_correlations c SET scope_id=t.scope_id FROM a2a_tasks t WHERE t.task_id=c.task_id;
ALTER TABLE a2a_message_correlations ALTER COLUMN scope_id SET NOT NULL;
ALTER TABLE a2a_message_correlations ADD FOREIGN KEY(scope_id) REFERENCES scopes(scope_id) ON DELETE CASCADE;

CREATE FUNCTION hosted_child_scope() RETURNS TRIGGER LANGUAGE plpgsql SET search_path=october_bus,pg_temp AS $$
BEGIN
 IF TG_TABLE_NAME='output_values' THEN
  SELECT scope_id INTO NEW.scope_id FROM output_streams WHERE stream_id=NEW.stream_id;
 ELSE
  SELECT scope_id INTO NEW.scope_id FROM a2a_tasks WHERE task_id=NEW.task_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosted_child_scope BEFORE INSERT OR UPDATE ON output_values FOR EACH ROW EXECUTE FUNCTION hosted_child_scope();
CREATE TRIGGER hosted_child_scope BEFORE INSERT OR UPDATE ON a2a_message_correlations FOR EACH ROW EXECUTE FUNCTION hosted_child_scope();

CREATE FUNCTION hosted_storage_change() RETURNS TRIGGER LANGUAGE plpgsql SET search_path=october_bus,pg_temp AS $$
DECLARE workspace TEXT; delta BIGINT:=0;
BEGIN
 IF TG_OP!='DELETE' THEN
  workspace:=NEW.scope_id;
  delta:=octet_length(to_jsonb(NEW)::text)+128;
 END IF;
 IF TG_OP!='INSERT' THEN
  workspace:=OLD.scope_id;
  delta:=delta-octet_length(to_jsonb(OLD)::text)-128;
 END IF;
 IF delta!=0 AND EXISTS(SELECT 1 FROM hosted_workspaces WHERE scope_id=workspace) THEN
  INSERT INTO hosted_storage_usage(scope_id,retained_bytes) VALUES(workspace,0) ON CONFLICT(scope_id) DO NOTHING;
  UPDATE hosted_storage_usage SET retained_bytes=retained_bytes+delta WHERE scope_id=workspace;
 END IF;
 RETURN NULL;
END $$;

-- Explicit table list: no identity secrets, payment records or file bytes.
DO $$
DECLARE name TEXT;
BEGIN
 INSERT INTO hosted_storage_usage(scope_id) SELECT scope_id FROM hosted_workspaces;
 FOREACH name IN ARRAY ARRAY['agents','messages','tasks','task_progress','escalations','events','channels','channel_posts','a2a_tasks','a2a_message_correlations','output_streams','output_values'] LOOP
  EXECUTE format('UPDATE hosted_storage_usage u SET retained_bytes=retained_bytes+x.bytes FROM (SELECT scope_id,SUM(octet_length(to_jsonb(t)::text)+128) AS bytes FROM %I t GROUP BY scope_id) x WHERE u.scope_id=x.scope_id',name);
  EXECUTE format('CREATE TRIGGER hosted_storage_change AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION hosted_storage_change()',name);
 END LOOP;
END $$;
