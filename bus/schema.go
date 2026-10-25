package bus

var schemaSQL = `
CREATE TABLE scopes (
  scope_id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	event_revision INTEGER NOT NULL DEFAULT 0,
	event_floor_revision INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE agents (
  scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL,
  display_name TEXT NOT NULL,
  capabilities_json TEXT NOT NULL,
  execution_id TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  lifecycle TEXT NOT NULL CHECK(lifecycle IN ('starting','ready','working','idle','needs_input','offline')),
  ready INTEGER NOT NULL CHECK(ready IN (0,1)),
  lease_expires_at INTEGER NOT NULL,
  registered_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(scope_id, agent_id)
);
CREATE INDEX agents_scope_updated ON agents(scope_id, updated_at DESC);
CREATE TABLE peer_links (
  scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  left_agent TEXT NOT NULL,
  right_agent TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(scope_id, left_agent, right_agent),
  CHECK(left_agent < right_agent),
  FOREIGN KEY(scope_id, left_agent) REFERENCES agents(scope_id, agent_id) ON DELETE CASCADE,
  FOREIGN KEY(scope_id, right_agent) REFERENCES agents(scope_id, agent_id) ON DELETE CASCADE
);
CREATE TABLE messages (
  message_id TEXT PRIMARY KEY,
  scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  from_kind TEXT NOT NULL CHECK(from_kind IN ('agent','a2aPrincipal','human')),
  from_id TEXT NOT NULL,
  to_kind TEXT NOT NULL CHECK(to_kind IN ('agent','a2aPrincipal','human')),
  to_id TEXT NOT NULL,
  mode TEXT NOT NULL CHECK(mode IN ('notify','request','response')),
  body TEXT NOT NULL,
  context_json TEXT NOT NULL,
  response_to TEXT,
  idempotency_key TEXT,
  request_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('queued','reserved','delivered','acknowledged','expired')),
  reservation_id TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER,
  delivered_at INTEGER,
  acknowledged_at INTEGER,
  replied_at INTEGER,
  response_message_id TEXT,
  FOREIGN KEY(response_to) REFERENCES messages(message_id)
);
CREATE INDEX messages_inbox ON messages(scope_id, to_kind, to_id, state, created_at);
CREATE INDEX messages_sender ON messages(scope_id, from_kind, from_id, created_at DESC);
CREATE UNIQUE INDEX messages_idempotency ON messages(scope_id, from_kind, from_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE TABLE reservations (
  reservation_id TEXT PRIMARY KEY,
  scope_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id) ON DELETE CASCADE
);
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY,
  scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	title TEXT NOT NULL,
  description TEXT NOT NULL,
	created_by TEXT,
  claimed_by TEXT,
  claimed_execution_id TEXT,
  status TEXT NOT NULL CHECK(status IN ('open','claimed','done')),
  dependencies_json TEXT NOT NULL,
  note TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
	CHECK((status='open' AND claimed_by IS NULL AND claimed_execution_id IS NULL) OR (status!='open' AND claimed_by IS NOT NULL AND claimed_execution_id IS NOT NULL)),
  FOREIGN KEY(scope_id, created_by) REFERENCES agents(scope_id, agent_id),
  FOREIGN KEY(scope_id, claimed_by) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX tasks_scope_status ON tasks(scope_id, status, created_at);
CREATE TABLE task_progress (
  task_id TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
  scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL CHECK(sequence>0),
  agent_id TEXT NOT NULL,
  execution_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('progress','note','blocker')),
  text TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(task_id, sequence),
  FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX task_progress_scope_task ON task_progress(scope_id, task_id, sequence);
CREATE TABLE escalations (
  escalation_id TEXT PRIMARY KEY,
  scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL,
  question TEXT NOT NULL,
  options_json TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('pending','resolved','cancelled')),
  answer TEXT,
  created_at INTEGER NOT NULL,
  resolved_at INTEGER,
  FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX escalations_scope_status ON escalations(scope_id, status, created_at);
CREATE TABLE events (
	event_id TEXT PRIMARY KEY,
	scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	revision INTEGER NOT NULL CHECK(revision>0),
	event_type TEXT NOT NULL,
	subject_id TEXT NOT NULL,
	attributes_json TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE(scope_id, revision)
);
CREATE INDEX events_scope_revision ON events(scope_id, revision);
CREATE INDEX events_scope_created ON events(scope_id, created_at);
CREATE TABLE a2a_publications (
	publication_id TEXT PRIMARY KEY,
	scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	agent_id TEXT NOT NULL,
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(scope_id, agent_id),
	FOREIGN KEY(scope_id, agent_id) REFERENCES agents(scope_id, agent_id)
);
CREATE INDEX a2a_publications_scope_created ON a2a_publications(scope_id, created_at);
CREATE TABLE a2a_tasks (
	task_id TEXT PRIMARY KEY,
	scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	context_id TEXT NOT NULL,
	principal_id TEXT NOT NULL,
	publication_id TEXT NOT NULL REFERENCES a2a_publications(publication_id) ON DELETE CASCADE,
	target_agent_id TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('submitted','working','input-required','completed','failed','canceled','rejected')),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	FOREIGN KEY(scope_id,target_agent_id) REFERENCES agents(scope_id,agent_id)
);
CREATE INDEX a2a_tasks_principal_updated ON a2a_tasks(principal_id,updated_at DESC);
CREATE INDEX a2a_tasks_scope_state ON a2a_tasks(scope_id,state,updated_at DESC);
CREATE TABLE a2a_message_correlations (
	principal_id TEXT NOT NULL,
	client_message_id TEXT NOT NULL,
	task_id TEXT NOT NULL REFERENCES a2a_tasks(task_id) ON DELETE CASCADE,
	request_hash TEXT NOT NULL,
	bus_request_message_id TEXT NOT NULL UNIQUE REFERENCES messages(message_id),
	bus_response_message_id TEXT UNIQUE REFERENCES messages(message_id),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY(principal_id,client_message_id)
);
CREATE INDEX a2a_messages_task_created ON a2a_message_correlations(task_id,created_at,client_message_id);
CREATE TABLE scoped_credentials (
	credential_id TEXT PRIMARY KEY,
	scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	label TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX scoped_credentials_scope_created ON scoped_credentials(scope_id, created_at);
CREATE TABLE scoped_credential_grants (
	credential_id TEXT NOT NULL REFERENCES scoped_credentials(credential_id) ON DELETE CASCADE,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	permission TEXT NOT NULL,
	PRIMARY KEY(credential_id, resource_type, resource_id, permission)
);
CREATE INDEX scoped_credential_grants_resource ON scoped_credential_grants(resource_type, resource_id, permission);
CREATE TABLE output_streams (
	stream_id TEXT PRIMARY KEY,
	scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	retention_limit INTEGER NOT NULL CHECK(retention_limit BETWEEN 1 AND 10000),
	sequence INTEGER NOT NULL DEFAULT 0,
	floor_sequence INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(scope_id,name)
);
CREATE INDEX output_streams_scope_created ON output_streams(scope_id,created_at);
CREATE TABLE output_stream_publishers (
	stream_id TEXT NOT NULL REFERENCES output_streams(stream_id) ON DELETE CASCADE,
	scope_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	PRIMARY KEY(stream_id,agent_id),
	FOREIGN KEY(scope_id,agent_id) REFERENCES agents(scope_id,agent_id)
);
CREATE TABLE output_values (
	stream_id TEXT NOT NULL REFERENCES output_streams(stream_id) ON DELETE CASCADE,
	sequence INTEGER NOT NULL,
	producer_type TEXT NOT NULL CHECK(producer_type IN ('agent','principal')),
	producer_id TEXT NOT NULL,
	content_type TEXT NOT NULL CHECK(content_type IN ('text/plain','application/json')),
	value_json TEXT NOT NULL,
	reference_json TEXT,
	created_at INTEGER NOT NULL,
	PRIMARY KEY(stream_id,sequence)
);
CREATE INDEX output_values_stream_created ON output_values(stream_id,created_at);
CREATE TABLE output_rate_usage (
	scope_id TEXT NOT NULL REFERENCES scopes(scope_id) ON DELETE CASCADE,
	principal_type TEXT NOT NULL,
	principal_id TEXT NOT NULL,
	window_start INTEGER NOT NULL,
	publish_count INTEGER NOT NULL DEFAULT 0,
	read_count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(scope_id,principal_type,principal_id,window_start)
);
CREATE INDEX output_rate_usage_window ON output_rate_usage(window_start);
CREATE INDEX messages_scope_history ON messages(scope_id,created_at DESC,message_id DESC);
PRAGMA user_version=13;
` + nativeWakeSchemaSQL + channelsSchemaSQL + attachmentsSchemaSQL
