-- Dedicated remote MCP keys cannot start machine sessions or manage accounts.
ALTER TABLE hosted_devices DROP CONSTRAINT hosted_devices_kind_check;
ALTER TABLE hosted_devices ADD CONSTRAINT hosted_devices_kind_check
  CHECK (kind IN ('device','mcp','mcp_key'));
