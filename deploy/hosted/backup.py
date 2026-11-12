#!/usr/bin/env python3
"""Back up one hosted Bus schema and its immutable files to an encrypted repository.

Credentials use libpq/restic environment or private password files. No secret is
accepted as a command argument. Intended for the supplied Linux systemd unit.
"""
import datetime
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import stat
import subprocess
import tempfile
import time


def setting(name):
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError("Missing setting: " + name)
    return value


def private_path(value, directory=False):
    path = pathlib.Path(value)
    info = path.lstat()
    kind = stat.S_ISDIR if directory else stat.S_ISREG
    if not path.is_absolute() or not kind(info.st_mode) or info.st_mode & 0o077:
        raise RuntimeError("Backup paths must be absolute, private and not symlinks")
    return path


def command(args, timeout=600):
    result = subprocess.run(args, capture_output=True, timeout=timeout, check=False)
    if result.returncode:
        # Provider diagnostics may contain connection details. Keep stdout and
        # stderr out of service logs; a nonzero result never marks success.
        raise RuntimeError(pathlib.Path(args[0]).name + " failed; backup was not confirmed")
    return result.stdout


def run():
    os.umask(0o077)
    setting("RESTIC_REPOSITORY")
    private_path(setting("RESTIC_PASSWORD_FILE"))
    for name in ("PGHOST", "PGDATABASE", "PGUSER"):
        setting(name)
    if os.environ["PGHOST"] not in ("127.0.0.1", "::1", "localhost") and os.environ.get("PGSSLMODE") != "verify-full":
        raise RuntimeError("Remote backups require PGSSLMODE=verify-full")
    attachments = private_path(setting("OCTOBER_BUS_ATTACHMENTS_DIR"), directory=True)
    service_env = private_path(setting("OCTOBER_BUS_SERVICE_ENV"))
    binary = pathlib.Path(setting("OCTOBER_BUS_BINARY")).resolve(strict=True)
    if not binary.is_file():
        raise RuntimeError("Configured runtime binary is not a file")
    cache = private_path(setting("OCTOBER_BUS_BACKUP_CACHE"), directory=True)
    state = private_path(setting("OCTOBER_BUS_BACKUP_STATE"), directory=True)
    lock_path = private_path(str(attachments / ".backup.lock"))
    # The daemon creates the persistent lock with its own UID at startup.
    # O_NOFOLLOW closes the final-component symlink race after validation.
    lock = os.open(lock_path, os.O_RDWR | os.O_NOFOLLOW)
    try:
        deadline = time.monotonic() + 30
        while True:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise RuntimeError("File writer or another backup is still active")
                time.sleep(0.1)
        started = datetime.datetime.now(datetime.timezone.utc).isoformat()
        # Estimate scratch capacity before writing an uncompressed custom dump.
        size = int(command(["psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-c",
            "SELECT COALESCE(SUM(pg_total_relation_size(c.oid)),0) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='october_bus' AND c.relkind IN ('r','m')"]).strip())
        if shutil.disk_usage(cache).free < size * 2 + 1024**3:
            raise RuntimeError("Backup scratch space would consume the recovery reserve")
        with tempfile.TemporaryDirectory(prefix="snapshot-", dir=cache) as temporary:
            stage = pathlib.Path(temporary)
            command(["pg_dump", "--schema=october_bus", "--format=custom", "--compress=0",
                     "--no-owner", "--no-privileges", "--lock-wait-timeout=10s", "--file=" + str(stage / "bus.dump")])
            shutil.copy2(binary, stage / "october-bus")
            shutil.copyfile(service_env, stage / "hosted.env")
            with open(stage / "october-bus", "rb") as executable:
                runtime_digest = hashlib.file_digest(executable, "sha256").hexdigest()
            manifest = {"format": "october-bus.hosted-backup", "version": 1, "startedAt": started,
                        "database": "bus.dump", "attachmentsPath": str(attachments),
                        "runtimeSHA256": runtime_digest}
            (stage / "manifest.json").write_text(json.dumps(manifest) + "\n")
            output = command(["restic", "backup", "--json", "--tag", "october-bus-hosted",
                              "--exclude", str(attachments / ".lock"), "--exclude", str(lock_path),
                              str(stage), str(attachments)], timeout=1500)
            summaries = [json.loads(line) for line in output.splitlines() if line.strip()]
            snapshots = [line.get("snapshot_id") for line in summaries if line.get("message_type") == "summary"]
            if len(snapshots) != 1 or not isinstance(snapshots[0], str) or not re.fullmatch(r"[0-9a-f]{8,64}", snapshots[0]):
                raise RuntimeError("Restic did not confirm one completed snapshot")
            complete = {"snapshotId": snapshots[0], "startedAt": started,
                        "completedAt": datetime.datetime.now(datetime.timezone.utc).isoformat()}
            pending = state / "last-success.json.tmp"
            pending.write_text(json.dumps(complete) + "\n")
            os.replace(pending, state / "last-success.json")
            print("Hosted Bus backup completed: " + snapshots[0], flush=True)
    finally:
        os.close(lock)


if __name__ == "__main__":
    try:
        run()
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
        # Avoid exception strings from external libraries/commands: they can
        # include credentials or user filenames. RuntimeError uses fixed text.
        message = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        raise SystemExit("Hosted Bus backup failed: " + message)
