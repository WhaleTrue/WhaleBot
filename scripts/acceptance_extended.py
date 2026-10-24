"""Extended real-Docker cases, called after Acceptance.executor().

Only preprovisioned hello/task identities are used. Deployments and secrets are
created through authenticated capabilities; all Docker actions stay executor-owned.
"""
from __future__ import annotations

import base64
import copy
import http.cookiejar
import io
import json
import pathlib
import secrets
import tarfile
import urllib.error
import urllib.parse
import urllib.request


ROOT = pathlib.Path(__file__).resolve().parents[1]


def _download(test, artifact, expected=200):
    """Keep binary bytes intact and route to the artifact's declared provider."""
    path = urllib.parse.urlsplit(artifact["url"])
    assert not path.scheme and not path.netloc and not path.fragment, artifact["url"]
    assert path.path == "/executor/artifacts/download", artifact["url"]
    query = urllib.parse.parse_qs(path.query)
    assert query.get("id") == [artifact["id"]], query
    assert len(query.get("provider", [])) == 1, "artifact lacks an unambiguous origin"
    principal = query["provider"][0]
    candidates = [(node, cap) for node, cap in test.providers("artifact.export")
                  if node["identity"]["principal"] == principal]
    assert candidates, f"artifact origin unavailable: {principal}"
    node, capability = sorted(candidates, key=lambda item: item[0]["identity"]["instance_id"])[0]
    operation = next(op for op in capability["operations"]
                     if op["method"] == "GET" and op["path"] == path.path)
    identity = node["identity"]
    node_id = "/".join(urllib.parse.quote(identity[field], safe="")
                       for field in ("namespace", "principal", "name", "instance_id"))
    request = urllib.request.Request(
        test.base + "/api/call",
        data=json.dumps({"capability": "artifact.export", "operation": operation["name"],
                         "node": node_id, "path": artifact["url"], "input": {}}).encode(),
        headers={"Origin": test.base, "Content-Type": "application/json", "X-CSRF-Token": test.csrf},
        method="POST",
    )
    try:
        response = test.client.open(request, timeout=180)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        assert response.status == expected, f"artifact download HTTP {response.status}, expected {expected}"
        if expected == 200:
            assert response.headers.get_content_type() == "application/gzip", response.headers
            assert "attachment;" in response.headers.get("Content-Disposition", ""), response.headers
        return body


def run_extended(test):
    """Exercise real scopes, files, jobs, artifact origins, secrets and recovery."""
    marker = "extended-" + secrets.token_hex(8)
    template = json.loads((ROOT / "examples/hello/deployment.json").read_text(encoding="utf-8"))
    admin = (test.client, test.csrf)
    owned = {}
    fixture = {}
    secret_key = marker + "-secret"
    secret_value = secrets.token_urlsafe(32)
    binary = b"\x00\xff\xfe\x80\r\n" + marker.encode() + bytes(range(256))
    binary_path = marker + "/payload.bin"
    secret_created = False

    def containers(deployment):
        return [item for item in test.call("container.manage", "/executor/containers", "GET")
                if item.get("Labels", {}).get("io.whalemesh.deployment") == deployment]

    def deployment(deployment_id, scope, ttl=120, replicas=1):
        value = copy.deepcopy(template)
        value.update(id=deployment_id, scope=scope, ttl_seconds=ttl, replicas=replicas,
                     session_id=marker if scope == "session" else "",
                     task_id=marker if scope == "task" else "")
        value["network"]["name"] = "whalemesh_acceptance_net"
        value["node"]["name"] = deployment_id
        value["labels"]["acceptance"] = marker
        return value

    def reconcile(value, expected=200):
        # Failed reconciles also persist desired state and can allocate owned volumes.
        owned[value["id"]] = (test.client, test.csrf)
        result = test.call("deployment.reconcile", "/executor/deployments", "PUT", value, expected=expected)
        if expected == 200:
            assert result["reconciled"] is True, result
        return result

    def remove(deployment_id):
        original = (test.client, test.csrf)
        try:
            test.client, test.csrf = owned[deployment_id]
            test.call("deployment.reconcile", "/executor/deployments?id=" + deployment_id, "DELETE")
            test.wait(lambda: containers(deployment_id), lambda items: not items)
            del owned[deployment_id]
        finally:
            test.client, test.csrf = original

    def files(operation, path, **fields):
        return test.call("container.files", "/executor/files", data={
            "id": fixture["container"], "operation": operation, "path": path, **fields})

    def execute(command, **fields):
        result = test.call("container.exec", "/executor/exec", data={
            "id": fixture["container"], "command": command, **fields})
        assert result["state"] == "complete" and result["exit_code"] == 0, "container command failed"
        return result

    def greet():
        return test.call("hello.invoke", "/hello", data={"name": marker}, principal="hello-0")

    def assert_archive():
        body = _download(test, fixture["artifact"])
        with tarfile.open(fileobj=io.BytesIO(body), mode="r:gz") as archive:
            assert sorted(archive.getnames()) == [marker, binary_path], archive.getnames()
            entry = archive.getmember(binary_path)
            assert entry.isfile(), entry.name
            with archive.extractfile(entry) as payload:
                assert payload.read() == binary, "downloaded artifact corrupts binary data"

    def files_jobs_artifacts_recovery():
        nonlocal secret_created
        owner = test.request("/api/auth/me")["owner"]
        test.call("secrets.resolve", "/secrets/put", "PUT", {"key": secret_key, "value": secret_value})
        secret_created = True
        value = deployment("hello", "global", ttl=1)
        value["secrets"] = [{"name": secret_key, "target": "/run/secrets/acceptance"}]
        reconcile(value)
        found = test.wait(lambda: containers("hello"), lambda items: len(items) == 1 and items[0]["State"] == "running")
        fixture["container"] = found[0]["Id"]
        test.wait(greet, lambda result: result == {"message": "Hello, " + marker + "!"})
        mounted = execute(["/bin/cat", "/run/secrets/acceptance"])
        assert mounted.get("stdout") == secret_value, "user-owned secret was not mounted correctly"
        assert secret_value not in json.dumps(test.topology()), "secret leaked into topology"
        listing = test.call("deployment.reconcile", "/executor/deployments", "GET")
        assert secret_value not in json.dumps(listing), "secret leaked into desired state"
        test.call("secrets.resolve", "/secrets/resolve", data={"key": secret_key}, expected=403)

        result = files("write", "/workspace/" + binary_path, content_base64=base64.b64encode(binary).decode())
        assert result["size"] == len(binary), result
        result = files("read", binary_path)
        assert result["size"] == len(binary) and base64.b64decode(result["content_base64"], validate=True) == binary
        for path in ("../escape", "/etc/passwd", "/workspace/../../escape", "nested\\..\\escape"):
            test.call("container.files", "/executor/files", data={
                "id": fixture["container"], "operation": "read", "path": path}, expected=400)
        test.call("container.files", "/executor/files", data={
            "id": fixture["container"], "operation": "move", "path": binary_path, "to": "../escape"}, expected=400)
        execute(["/bin/ln", "-s", "/run/secrets", "/workspace/" + marker + "-escape"])
        for operation in ("read", "write"):
            test.call("container.files", "/executor/files", data={
                "id": fixture["container"], "operation": operation, "path": marker + "-escape/acceptance",
                "content_base64": base64.b64encode(b"must-not-write").decode()}, expected=400)
        assert execute(["/bin/cat", "/run/secrets/acceptance"]).get("stdout") == secret_value, "escaped write changed secret"

        job = test.call("container.exec", "/executor/exec", data={
            "id": fixture["container"], "command": ["/bin/sh", "-c", 'sleep 1; printf "%s" "$ACCEPTANCE_MARKER"'],
            "cwd": "/workspace/" + marker, "env": {"ACCEPTANCE_MARKER": marker},
            "timeout_seconds": 20, "async": True}, expected=202)
        fixture["job"] = job["id"]
        completed = test.wait(lambda: test.call("container.exec", "/executor/jobs?id=" + job["id"], "GET"),
                              lambda item: item["state"] in ("complete", "failed", "interrupted"))
        assert completed["state"] == "complete" and completed["exit_code"] == 0, completed
        assert completed.get("stdout") == marker and not completed.get("stderr"), completed
        exporter = sorted(test.providers("artifact.export"), key=lambda item: item[0]["identity"]["instance_id"])[0][0]
        artifact = test.call("artifact.export", "/executor/artifacts/export", data={
            "id": fixture["container"], "path": "/workspace/" + marker}, expected=201)
        assert artifact["owner"] == owner and artifact["media_type"] == "application/gzip", artifact
        assert urllib.parse.parse_qs(urllib.parse.urlsplit(artifact["url"]).query).get("provider") == [exporter["identity"]["principal"]]
        fixture["artifact"] = artifact
        assert_archive()

        previous = next(node["identity"]["instance_id"] for node, _ in test.providers("hello.invoke")
                        if node["identity"]["principal"] == "hello-0")
        test.call("container.manage", "/executor/containers/action", data={"id": fixture["container"], "action": "restart"})
        test.wait(lambda: test.providers("hello.invoke"), lambda items: any(
            node["identity"]["principal"] == "hello-0" and node["identity"]["instance_id"] != previous for node, _ in items))
        test.wait(greet, lambda result: result == {"message": "Hello, " + marker + "!"})
        assert base64.b64decode(files("read", binary_path)["content_base64"], validate=True) == binary
        assert execute(["/bin/cat", "/run/secrets/acceptance"]).get("stdout") == secret_value, "restart lost secret mount"
        previous_executor = exporter["identity"]["instance_id"]
        test.compose("restart", "executor")
        test.wait(lambda: test.providers("deployment.reconcile"), lambda items: any(
            node["identity"]["principal"] == exporter["identity"]["principal"]
            and node["identity"]["instance_id"] != previous_executor for node, _ in items))
        test.wait(lambda: containers("hello"), lambda items: len(items) == 1 and items[0]["Id"] == fixture["container"])
        recovered = test.wait(lambda: test.call("container.exec", "/executor/jobs?id=" + job["id"], "GET"),
                              lambda item: item["state"] == "complete")
        assert recovered["exit_code"] == 0 and recovered.get("stdout") == marker, recovered
        assert base64.b64decode(files("read", binary_path)["content_base64"], validate=True) == binary
        assert_archive()

    def cross_user_secret_isolation():
        admin_owner = test.request("/api/auth/me")["owner"]
        username, password = marker + "-user", secrets.token_urlsafe(24)
        test.request("/api/users", {"username": username, "password": password, "scopes": [
            "mesh.inspect", "deployment.reconcile", "container.manage", "container.exec", "container.files",
            "artifact.export", "secrets.read", "secrets.write"]}, method="PUT", expected=201)
        try:
            test.client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            login = test.request("/api/auth/login", {"username": username, "password": password})
            test.csrf = login["csrf"]
            assert login["owner"] != admin_owner
            listed = test.call("secrets.resolve", "/secrets/list", data={"owner": admin_owner})
            assert all(item["key"] != secret_key for item in listed["items"]), "foreign secret metadata visible"
            assert containers("hello") == [], "browser delegation retains administrator access"
            test.call("container.exec", "/executor/exec", data={
                "id": fixture["container"], "command": ["/bin/cat", "/run/secrets/acceptance"], "owner": admin_owner}, expected=400)
            _download(test, fixture["artifact"], expected=400)
            other = (test.client, test.csrf)
            foreign_key = marker + "-foreign"
            test.call("secrets.resolve", "/secrets/put", "PUT", {
                "key": foreign_key, "value": secrets.token_urlsafe(32)})
            try:
                # Even the administrator's browser call delegates only its own user.
                # Reuse task under the administrator: absent records retain ownership.
                test.client, test.csrf = admin
                value = deployment("task", "task")
                value["secrets"] = [{"name": foreign_key, "target": "/run/secrets/acceptance"}]
                error = reconcile(value, expected=400)
                assert "404" in json.dumps(error) or "not found" in json.dumps(error).lower(), error
                assert containers("task") == [], "foreign secret reference created a container"
                remove("task")
            finally:
                test.client, test.csrf = other
                test.call("secrets.resolve", "/secrets/delete", data={"key": foreign_key})
        finally:
            test.client, test.csrf = admin
        assert execute(["/bin/cat", "/run/secrets/acceptance"]).get("stdout") == secret_value, "foreign user changed secret"

    def scopes_and_ttl():
        def expired():
            records = test.call("deployment.reconcile", "/executor/deployments", "GET")
            record = next(item for item in records if item["deployment"]["id"] == "task")
            return record["deployment"]["desired_state"] == "absent" and not containers("task")

        value = deployment("task", "session", ttl=2)
        reconcile(value)
        test.wait(lambda: containers("task"), lambda items: len(items) == 1 and items[0]["State"] == "running")
        test.wait(expired, bool, timeout=100)
        # Session expiry proves a sweeper ran. Idle global TTL=1 must not expire.
        assert len(containers("hello")) == 1, "global deployment expired with session TTL"
        remove("task")

        value = deployment("task", "task", replicas=2)
        reconcile(value)
        test.wait(lambda: containers("task"), lambda items: len(items) == 2 and all(item["State"] == "running" for item in items))
        value["replicas"] = 1
        reconcile(value)
        test.wait(lambda: containers("task"), lambda items: len(items) == 1 and items[0]["State"] == "running")
        value["desired_state"] = "stopped"
        reconcile(value)
        test.wait(lambda: containers("task"), lambda items: len(items) == 1 and items[0]["State"] == "exited")
        value.update(desired_state="running", ttl_seconds=2)
        reconcile(value)
        test.wait(lambda: containers("task"), lambda items: len(items) == 1 and items[0]["State"] == "running")
        test.wait(expired, bool, timeout=100)
        assert len(containers("hello")) == 1, "global deployment expired with task TTL"
        remove("task")
        remove("hello")

    try:
        test.check("extended binary workspace, async job, artifact origin, secret mount and restart recovery", files_jobs_artifacts_recovery)
        test.check("extended cross-user secret deployment and artifact isolation", cross_user_secret_isolation)
        test.check("extended session/global/task lifecycle, replicas and actual idle TTL", scopes_and_ttl)
    finally:
        cleanup_errors = []
        for deployment_id in list(owned):
            try:
                remove(deployment_id)
            except Exception as error:
                cleanup_errors.append(f"{deployment_id}: {error}")
        test.client, test.csrf = admin
        if secret_created:
            try:
                test.call("secrets.resolve", "/secrets/delete", data={"key": secret_key})
            except Exception as error:
                cleanup_errors.append(f"secret cleanup: {error}")
        if cleanup_errors:
            raise RuntimeError("extended acceptance cleanup failed: " + "; ".join(cleanup_errors))
