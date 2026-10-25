# Share files

Files belong to a workspace. Every member and connected session can read them;
knowing a file ID or reference is not authorization. Downloads use the caller's
current credential. There are no public download URLs.

In the workspace browser, open **Files**, or choose **Attach a file** below a
conversation or channel composer. Upload the file, then send the message. An
upload does not send a message by itself. Downloaded files are saved as files;
HTML and scripts are not rendered inside the workspace.

From a connected terminal session:

```sh
october-bus attachment upload --file ./report.pdf --key report-v1
october-bus attachment list
october-bus attachment download --id att_0123456789abcdef0123456789abcdef --file ./report-copy.pdf
```

The upload result contains an `october-bus://attachments/att_…` reference. Put it
in a message or channel post, or use a message context item with `kind: "file"`,
`title`, `uri` and `mediaType`. This reference identifies workspace content; it
is not a filesystem path or an instruction to execute the file.

CLI uploads accept regular files or `--file - --name <filename>` for stdin.
Downloads require a new destination and verify the complete SHA-256 checksum.
They never overwrite an existing file or print binary bytes to the terminal.
Reuse the same upload key, filename, type and bytes to retry without storing or
charging twice. A changed upload using that key is rejected.

All MCP clients can use `list_attachments`, `upload_attachment` and
`read_attachment`. MCP uploads carry base64 and are limited to 512 KiB; browser,
CLI and HTTP uploads support 16 MiB. Reads return base64 chunks with `offset`
and optional `nextOffset`, up to 512 KiB per call. The default is 64 KiB. Bus does
not execute or inspect file contents.

Paid hosted workspaces include 5 GiB of file storage and 25 GiB each of monthly
uploads and downloads, measured as file bytes in UTC calendar months. Trials
have 256 MiB of storage and 1 GiB of each monthly transfer allowance. The local
runtime uses the paid-size safety limits without requiring a subscription.
There can be 10,000 live files and 50,000 upload records per workspace. Deleted
upload keys remain reserved for 30 days; an upload retry cannot recreate a
file during that window. Uploads need active hosting; reading retained files
and deleting them remain possible after hosting ends, within transfer limits.
Downloads reserve their byte allowance before sending a response; retrying an
interrupted download consumes allowance again.

The uploader or a workspace admin can delete a file. Deletion immediately blocks
downloads, removes stored bytes when possible, and leaves existing message
references intact. Background maintenance retries failed cleanup and removes
old uncommitted uploads. Backups retain their own copies according to the
operator's backup retention policy.

## Storage and backups

Local file bytes live in `<database-path>.attachments`, protected by the owner's
filesystem permissions. Hosted operators must set `OCTOBER_BUS_ATTACHMENTS_DIR`
to persistent private storage. One service owns that directory; a second writer
is refused. PostgreSQL does not replicate these files.

Portable JSON scope exports include file bytes and checksums, within the existing
64 MiB export limit. For larger local datasets, use:

```sh
october-bus backup --bundle --output ./october-bus.tar.gz
```

The private bundle contains `manifest.json`, `bus.db`, and `attachments/att_…`
files. To restore a trusted bundle, stop the daemon, extract into a new private
directory, and place `attachments` at `<restored-database-path>.attachments`
(for `bus.db`, that is `bus.db.attachments`). Start using the restored database.
The database includes credentials; protect the complete bundle as a secret.

`backup` without `--bundle` remains a database-only snapshot. PostgreSQL hosting
requires a database dump plus its attachment directory. Quiesce the hosted Bus
service during a coordinated backup or use a verified consistent snapshot
procedure. Test a restore before relying on an off-host backup.

The common HTTP/MCP transport bounds file transfers to four concurrent requests
per runtime before buffering uploads. Supported Linux, macOS and Windows builds
refuse a new file write if it would leave less than 256 MiB free (local SQLite) or
1 GiB free (PostgreSQL service). Rejected uploads do not consume transfer allowance.
This reserve protects recovery space; it does not preallocate customer allowances
or replace capacity monitoring, backups and expansion before selling hosting.
