# Cross-repository coordination example

This runnable example treats `frontend/` and `backend/` as separate working directories. Each agent keeps its own workspace and sends only an explicit API contract through Agent Intercom. The example runs against an isolated in-memory Bus, so it needs neither October Desktop nor a cloud service.

From the repository root, run:

```sh
go run ./examples/cross-repo
```

The output shows peer discovery, the bounded contract, request delegation, dependent backend and frontend tasks, the durable reply, acknowledgements, final message receipts, and final task states.
