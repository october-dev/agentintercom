# Live output stream example

This page reads ordered coding-agent output without registering as an agent.

1. Create an output stream and a read-only output principal with a Go or TypeScript client.
2. Start Agent Intercom with this page's exact origin in `OCTOBER_BUS_ALLOWED_ORIGINS`.
3. Serve this directory with any static web server.
4. Open the page and enter the Bus address, stream ID, and read credential.

For a server on port 8080, start the Bus with:

```sh
OCTOBER_BUS_ALLOWED_ORIGINS=http://127.0.0.1:8080 agentintercom start
```

The page sends the credential only in the `Authorization` header. It does not put it in the URL or save it in browser storage.
