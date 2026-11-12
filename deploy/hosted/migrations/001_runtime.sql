-- Standalone Bus coordination tables. Applied transactionally with search_path=october_bus,pg_catalog.

CREATE TABLE scopes (
  scope_id TEXT COLLATE "C" PRIMARY KEY,
  token_hash TEXT COLLATE "C" NOT NULL UNIQUE,
	created_at BIGINT NOT NULL,
	event_revision BIGINT NOT NULL DEFAULT 0,
	event_floor_revision BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE agents (
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  agent_id TEXT COLLATE "C" NOT NULL,
  display_name TEXT COLLATE "C" NOT NULL,
  capabilities_json TEXT COLLATE "C" NOT NULL,
  execution_id TEXT COLLATE "C" NOT NULL,
  token_hash TEXT COLLATE "C" NOT NULL UNIQUE,
  lifecycle TEXT COLLATE "C" NOT NULL CHECK(lifecycle IN ('starting','ready','working','idle','needs_input','offline')),
  ready BIGINT NOT NULL CHECK(ready IN (0,1)),
  lease_expires_at BIGINT NOT NULL,
  registered_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY(scope_id, agent_id)
);
CREATE INDEX agents_scope_updated ON agents(scope_id, updated_at DESC);
CREATE TABLE peer_links (
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  left_agent TEXT COLLATE "C" NOT NULL,
  right_agent TEXT COLLATE "C" NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY(scope_id, left_agent, right_agent),
  CHECK(left_agent < right_agent),
  FOREIGN KEY(scope_id, left_agent) REFERENCES agents(scope_id, agent_id) ON DELETE CASCADE,
  FOREIGN KEY(scope_id, right_agent) REFERENCES agents(scope_id, agent_id) ON DELETE CASCADE
);
CREATE TABLE messages (
  message_id TEXT COLLATE "C" PRIMARY KEY,
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  from_kind TEXT COLLATE "C" NOT NULL CHECK(from_kind IN ('agent','a2aPrincipal')),
  from_id TEXT COLLATE "C" NOT NULL,
  to_kind TEXT COLLATE "C" NOT NULL CHECK(to_kind IN ('agent','a2aPrincipal')),
  to_id TEXT COLLATE "C" NOT NULL,
  mode TEXT COLLATE "C" NOT NULL CHECK(mode IN ('notify','request','response')),
  body TEXT COLLATE "C" NOT NULL,
  context_json TEXT COLLATE "C" NOT NULL,
  response_to TEXT COLLATE "C",
  idempotency_key TEXT COLLATE "C",
  request_hash TEXT COLLATE "C" NOT NULL,
  state TEXT COLLATE "C" NOT NULL CHECK(state IN ('queued','reserved','delivered','acknowledged','expired')),
  reservation_id TEXT COLLATE "C",
  created_at BIGINT NOT NULL,
  expires_at BIGINT,
  delivered_at BIGINT,
  acknowledged_at BIGINT,
  replied_at BIGINT,
  response_message_id TEXT COLLATE "C",
  FOREIGN KEY(response_to) REFERENCES messages(message_id)
);
CREATE INDEX messages_inbox ON messages(scope_id, to_kind, to_id, state, created_at);
CREATE INDEX messages_sender ON messages(scope_id, from_kind, from_id, created_at DESC);
CREATE UNIQUE INDEX messages_idempotency ON messages(scope_id, from_kind, from_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE TABLE reservations (
  reservation_id TEXT COLLATE "C" PRIMARY KEY,
  scope_id TEXT COLLATE "C" NOT NULL,
  agent_id TEXT COLLATE "C" NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id) ON DELETE CASCADE
);
CREATE TABLE tasks (
  task_id TEXT COLLATE "C" PRIMARY KEY,
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	title TEXT COLLATE "C" NOT NULL,
  description TEXT COLLATE "C" NOT NULL,
	created_by TEXT COLLATE "C",
  claimed_by TEXT COLLATE "C",
  claimed_execution_id TEXT COLLATE "C",
  status TEXT COLLATE "C" NOT NULL CHECK(status IN ('open','claimed','done')),
  dependencies_json TEXT COLLATE "C" NOT NULL,
  note TEXT COLLATE "C",
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
	CHECK((status='open' AND claimed_by IS NULL AND claimed_execution_id IS NULL) OR (status!='open' AND claimed_by IS NOT NULL AND claimed_execution_id IS NOT NULL)),
  FOREIGN KEY(scope_id, created_by) REFERENCES agents(scope_id, agent_id),
  FOREIGN KEY(scope_id, claimed_by) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX tasks_scope_status ON tasks(scope_id, status, created_at);
CREATE TABLE task_progress (
  task_id TEXT COLLATE "C" NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  sequence BIGINT NOT NULL CHECK(sequence>0),
  agent_id TEXT COLLATE "C" NOT NULL,
  execution_id TEXT COLLATE "C" NOT NULL,
  kind TEXT COLLATE "C" NOT NULL CHECK(kind IN ('progress','note','blocker')),
  text TEXT COLLATE "C" NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY(task_id, sequence),
  FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX task_progress_scope_task ON task_progress(scope_id, task_id, sequence);
CREATE TABLE escalations (
  escalation_id TEXT COLLATE "C" PRIMARY KEY,
  scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  agent_id TEXT COLLATE "C" NOT NULL,
  question TEXT COLLATE "C" NOT NULL,
  options_json TEXT COLLATE "C" NOT NULL,
  status TEXT COLLATE "C" NOT NULL CHECK(status IN ('pending','resolved','cancelled')),
  answer TEXT COLLATE "C",
  created_at BIGINT NOT NULL,
  resolved_at BIGINT,
  FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX escalations_scope_status ON escalations(scope_id, status, created_at);
CREATE TABLE events (
	event_id TEXT COLLATE "C" PRIMARY KEY,
	scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	revision BIGINT NOT NULL CHECK(revision>0),
	event_type TEXT COLLATE "C" NOT NULL,
	subject_id TEXT COLLATE "C" NOT NULL,
	attributes_json TEXT COLLATE "C" NOT NULL,
	created_at BIGINT NOT NULL,
	UNIQUE(scope_id, revision)
);
CREATE INDEX events_scope_revision ON events(scope_id, revision);
CREATE INDEX events_scope_created ON events(scope_id, created_at);
CREATE TABLE a2a_publications (
	publication_id TEXT COLLATE "C" PRIMARY KEY,
	scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	agent_id TEXT COLLATE "C" NOT NULL,
	enabled BIGINT NOT NULL CHECK(enabled IN (0,1)),
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	UNIQUE(scope_id, agent_id),
	FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX a2a_publications_scope_created ON a2a_publications(scope_id, created_at);
CREATE TABLE a2a_tasks (
	task_id TEXT COLLATE "C" PRIMARY KEY,
	scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	context_id TEXT COLLATE "C" NOT NULL,
	principal_id TEXT COLLATE "C" NOT NULL,
	publication_id TEXT COLLATE "C" NOT NULL REFERENCES a2a_publications(publication_id) ON DELETE CASCADE,
	target_agent_id TEXT COLLATE "C" NOT NULL,
	state TEXT COLLATE "C" NOT NULL CHECK(state IN ('submitted','working','input-required','completed','failed','canceled','rejected')),
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	FOREIGN KEY(scope_id,target_agent_id) REFERENCES agents(scope_id,agent_id)
);
CREATE INDEX a2a_tasks_principal_updated ON a2a_tasks(principal_id,updated_at DESC);
CREATE INDEX a2a_tasks_scope_state ON a2a_tasks(scope_id,state,updated_at DESC);
CREATE TABLE a2a_message_correlations (
	principal_id TEXT COLLATE "C" NOT NULL,
	client_message_id TEXT COLLATE "C" NOT NULL,
	task_id TEXT COLLATE "C" NOT NULL REFERENCES a2a_tasks(task_id) ON DELETE CASCADE,
	request_hash TEXT COLLATE "C" NOT NULL,
	bus_request_message_id TEXT COLLATE "C" NOT NULL UNIQUE REFERENCES messages(message_id),
	bus_response_message_id TEXT COLLATE "C" UNIQUE REFERENCES messages(message_id),
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY(principal_id,client_message_id)
);
CREATE INDEX a2a_messages_task_created ON a2a_message_correlations(task_id,created_at,client_message_id);
CREATE TABLE scoped_credentials (
	credential_id TEXT COLLATE "C" PRIMARY KEY,
	scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	label TEXT COLLATE "C" NOT NULL,
	token_hash TEXT COLLATE "C" NOT NULL UNIQUE,
	enabled BIGINT NOT NULL CHECK(enabled IN (0,1)),
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE INDEX scoped_credentials_scope_created ON scoped_credentials(scope_id, created_at);
CREATE TABLE scoped_credential_grants (
	credential_id TEXT COLLATE "C" NOT NULL REFERENCES scoped_credentials(credential_id) ON DELETE CASCADE,
	resource_type TEXT COLLATE "C" NOT NULL,
	resource_id TEXT COLLATE "C" NOT NULL,
	permission TEXT COLLATE "C" NOT NULL,
	PRIMARY KEY(credential_id, resource_type, resource_id, permission)
);
CREATE INDEX scoped_credential_grants_resource ON scoped_credential_grants(resource_type, resource_id, permission);
CREATE TABLE output_streams (
	stream_id TEXT COLLATE "C" PRIMARY KEY,
	scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	name TEXT COLLATE "C" NOT NULL,
	retention_limit BIGINT NOT NULL CHECK(retention_limit BETWEEN 1 AND 10000),
	sequence BIGINT NOT NULL DEFAULT 0,
	floor_sequence BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	UNIQUE(scope_id,name)
);
CREATE INDEX output_streams_scope_created ON output_streams(scope_id,created_at);
CREATE TABLE output_stream_publishers (
	stream_id TEXT COLLATE "C" NOT NULL REFERENCES output_streams(stream_id) ON DELETE CASCADE,
	scope_id TEXT COLLATE "C" NOT NULL,
	agent_id TEXT COLLATE "C" NOT NULL,
	PRIMARY KEY(stream_id,agent_id),
	FOREIGN KEY(scope_id,agent_id) REFERENCES agents(scope_id,agent_id)
);
CREATE TABLE output_values (
	stream_id TEXT COLLATE "C" NOT NULL REFERENCES output_streams(stream_id) ON DELETE CASCADE,
	sequence BIGINT NOT NULL,
	producer_type TEXT COLLATE "C" NOT NULL CHECK(producer_type IN ('agent','principal')),
	producer_id TEXT COLLATE "C" NOT NULL,
	content_type TEXT COLLATE "C" NOT NULL CHECK(content_type IN ('text/plain','application/json')),
	value_json TEXT COLLATE "C" NOT NULL,
	reference_json TEXT COLLATE "C",
	created_at BIGINT NOT NULL,
	PRIMARY KEY(stream_id,sequence)
);
CREATE INDEX output_values_stream_created ON output_values(stream_id,created_at);
CREATE TABLE output_rate_usage (
	scope_id TEXT COLLATE "C" NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	principal_type TEXT COLLATE "C" NOT NULL,
	principal_id TEXT COLLATE "C" NOT NULL,
	window_start BIGINT NOT NULL,
	publish_count BIGINT NOT NULL DEFAULT 0,
	read_count BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY(scope_id,principal_type,principal_id,window_start)
);
CREATE INDEX output_rate_usage_window ON output_rate_usage(window_start);


CREATE INDEX IF NOT EXISTS messages_expiry ON messages(scope_id,expires_at) WHERE expires_at IS NOT NULL AND state NOT IN ('acknowledged','expired');
CREATE INDEX IF NOT EXISTS messages_active ON messages(scope_id,state) WHERE state NOT IN ('acknowledged','expired');
CREATE INDEX IF NOT EXISTS messages_remote_active ON messages(scope_id,from_kind,state) WHERE state NOT IN ('acknowledged','expired');
CREATE INDEX IF NOT EXISTS tasks_active ON tasks(scope_id,status) WHERE status!='done';
CREATE INDEX IF NOT EXISTS tasks_claims ON tasks(scope_id,status,claimed_by,claimed_execution_id);
CREATE INDEX IF NOT EXISTS tasks_page ON tasks(scope_id,task_id);
