"""Real Docker acceptance. Own project only; no external credentials required.

Run after builds/unit tests: python scripts/acceptance.py
Artifacts contain test outcomes and container logs, never bootstrap passwords.
"""
from __future__ import annotations

import argparse
import base64
import http.cookiejar
import json
import pathlib
import secrets
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

from acceptance_extended import run_extended

ROOT = pathlib.Path(__file__).resolve().parents[1]


class Acceptance:
    def __init__(self, project: str, base: str | None):
        self.project, self.base = project, base
        self.command = ["docker", "compose", "-p", project, "-f", "docker-compose.yml", "-f", "deployment/acceptance.yml"]
        self.client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.csrf = ""
        self.results = []
        self.output = ROOT / "artifacts" / "acceptance"
        self.output.mkdir(parents=True, exist_ok=True)

    def compose(self, *args, check=True):
        result = subprocess.run(self.command + list(args), cwd=ROOT, text=True, capture_output=True, timeout=900)
        if check and result.returncode:
            raise RuntimeError(f"compose {' '.join(args)}: {result.stderr[-3000:]}")
        return result

    def request(self, path, data=None, method=None, expected=200, client=None):
        headers = {"Origin": self.base}
        if data is not None:
            headers.update({"Content-Type": "application/json", "X-CSRF-Token": self.csrf})
        request = urllib.request.Request(self.base + path, data=None if data is None else json.dumps(data).encode(), headers=headers, method=method)
        try:
            response = (client or self.client).open(request, timeout=180)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read()
            if response.status != expected:
                raise AssertionError(f"{path}: HTTP {response.status}, expected {expected}: {body[:1000]!r}")
            if "application/json" in response.headers.get("Content-Type", ""):
                return json.loads(body)
            return body.decode()

    def wait(self, action, predicate, timeout=70):
        until = time.monotonic() + timeout
        last = None
        while time.monotonic() < until:
            try:
                last = action()
                if predicate(last):
                    return last
            except (OSError, AssertionError) as error:
                last = str(error)
            time.sleep(0.5)
        raise AssertionError(f"condition timed out: {last!r}")

    def topology(self):
        return self.request("/api/topology")

    def manifests(self):
        state = self.topology()
        return [state["manifest"]] + [peer["manifest"] for peer in state["peers"]]

    def providers(self, capability):
        return [(manifest, cap) for manifest in self.manifests() for cap in manifest.get("provides", []) if cap["id"] == capability and cap.get("ready") and manifest["lifecycle"].get("ready") and not manifest["lifecycle"].get("draining")]

    def call(self, capability, path, method="POST", data=None, principal=None, expected=200, stream=False):
        candidates = self.providers(capability)
        if principal:
            candidates = [(node, cap) for node, cap in candidates if node["identity"]["principal"] == principal]
        if not candidates:
            raise AssertionError(f"provider missing: {capability} {principal}")
        node, cap = sorted(candidates, key=lambda item: item[0]["identity"]["instance_id"])[0]
        clean_path = urllib.parse.urlparse(path).path
        operation = next(op for op in cap["operations"] if op["method"] == method and op["path"] == clean_path)
        identity = node["identity"]
        node_id = "/".join(urllib.parse.quote(identity[field], safe="") for field in ["namespace", "principal", "name", "instance_id"])
        return self.request("/api/call", {"capability": capability, "operation": operation["name"], "node": node_id, "path": path, "input": data or {}, "stream": stream}, expected=expected)

    def check(self, name, action):
        started = time.monotonic()
        try:
            action()
        except Exception as error:
            self.results.append({"case": name, "passed": False, "seconds": round(time.monotonic()-started, 3), "error": str(error)})
            raise
        else:
            self.results.append({"case": name, "passed": True, "seconds": round(time.monotonic()-started, 3)})
            print(f"PASS {name}", flush=True)
        finally:
            (self.output / "results.json").write_text(json.dumps(self.results, indent=2), encoding="utf-8")

    def chat(self, marker):
        output = self.call("agent.run", "/agent/run", data={"session_id": "acceptance-" + secrets.token_hex(6), "message": marker})
        assert marker in output["reply"] and "MOCK" in output["reply"], output
        return output

    def invoke_mock(self, principal, marker):
        catalog = self.wait(lambda: self.call("llm.models.list", "/llm/models", "GET", principal=principal), lambda value: any(model["id"] == "mock" and model["cost_class"] == "free" for model in value["models"]))
        # Retry only safe catalog reads, never inference.
        return self.call("llm.invoke", "/llm/invoke", data={"model": "mock", "model_revision": catalog["revision"], "free_only": True, "messages": [{"role": "user", "content": marker}]}, principal=principal)

    def start(self):
        self.compose("up", "-d", "--build")
        if self.base is None:
            self.base = "http://" + self.compose("port", "web", "8080").stdout.strip()
        self.wait(lambda: urllib.request.urlopen(self.base + "/healthz", timeout=3).status, lambda status: status == 200)
        password = self.compose("exec", "-T", "web", "cat", "/identity/admin-password").stdout.strip()
        login = self.request("/api/auth/login", {"username": "admin", "password": password})
        self.csrf = login["csrf"]
        def ready(nodes):
            for principal in ["agent", "llm", "store", "knowledge", "web"]:
                live = [node for node in nodes if node["identity"]["principal"] == principal and not node["lifecycle"].get("draining")]
                if len(live) != 1 or not live[0]["lifecycle"].get("ready"):
                    return False
            return True
        self.wait(self.manifests, ready)

    def minimal(self):
        command = ["docker", "compose", "-p", self.project + "-minimal", "-f", "compose.minimal.yml"]
        try:
            subprocess.run(command + ["up", "-d", "--build"], cwd=ROOT, check=True, capture_output=True, timeout=900)
            marker = "minimal-" + secrets.token_hex(6)
            result = subprocess.run(command + ["run", "--rm", "-T", "chat", marker], cwd=ROOT, text=True, check=True, capture_output=True, timeout=180)
            assert marker in result.stdout and "MOCK" in result.stdout, result.stdout
        finally:
            subprocess.run(command + ["--profile", "cli", "down"], cwd=ROOT, capture_output=True, timeout=120)

    def dynamic_knowledge(self):
        self.compose("stop", "knowledge")
        self.wait(lambda: self.providers("memory.search"), lambda providers: not providers)
        self.chat("without-knowledge-" + secrets.token_hex(6))
        self.compose("start", "knowledge")
        self.wait(lambda: self.providers("memory.search"), bool)
        marker = "knowledge-" + secrets.token_hex(6)
        # Membership is eventually consistent; prove read reachability before a mutation.
        self.wait(lambda: self.call("memory.search", "/memory/search", data={"query": marker}), lambda result: all(note["key"] != marker for note in result["items"]))
        self.call("memory.store", "/memory/put", "PUT", {"key": marker, "value": marker})
        found = self.call("memory.search", "/memory/search", data={"query": marker})
        assert any(note["key"] == marker and note["value"] == marker for note in found["items"]), found
        skill = {"id": marker, "version": "1.0.0", "title": marker, "description": marker, "tags": ["acceptance"], "files": [{"path": "SKILL.md", "content": base64.b64encode(marker.encode()).decode()}]}
        self.call("skills.search", "/skills/put", "PUT", skill)
        found = self.call("skills.search", "/skills/search", data={"query": marker})
        assert any(item["id"] == marker for item in found["items"]), found

    def multiple_and_failover(self):
        self.compose("--profile", "multi-provider", "up", "-d", "llm-second")
        self.wait(lambda: self.providers("llm.invoke"), lambda items: {node["identity"]["principal"] for node, _ in items} >= {"llm", "llm-second"})
        for principal in ["llm", "llm-second"]:
            marker = principal + "-" + secrets.token_hex(6)
            result = self.invoke_mock(principal, marker)
            assert marker in result["message"]["content"], result
        self.compose("stop", "llm")
        self.wait(lambda: self.providers("llm.invoke"), lambda items: all(node["identity"]["principal"] != "llm" for node, _ in items))
        self.chat("failover-" + secrets.token_hex(6))
        self.compose("start", "llm")
        self.wait(lambda: self.providers("llm.invoke"), lambda items: len(items) >= 2)

    def executor(self):
        self.compose("--profile", "executor", "up", "-d", "executor")
        self.compose("--profile", "example", "build", "hello")
        self.wait(lambda: self.providers("deployment.reconcile"), bool)
        self.wait(lambda: self.call("container.manage", "/executor/containers", "GET"), lambda containers: isinstance(containers, list))
        deployment = json.loads((ROOT / "examples/hello/deployment.json").read_text())
        deployment["network"]["name"] = "whalemesh_acceptance_net"
        deployment["node"]["requires"] = [{"id": "template-hello", "capability": "hello.invoke",
                                           "optional": True, "scopes": ["hello.invoke"]}]
        deployment["requires"] = [{"id": "deployment-hello", "capability": "hello.invoke",
                                   "optional": True, "scopes": ["hello.invoke"]}]
        result = self.call("deployment.reconcile", "/executor/deployments", "PUT", deployment)
        assert result["reconciled"] is True, result
        assert "manifest" not in result["deployment"], result
        providers = self.wait(lambda: self.providers("hello.invoke"), bool)
        child = next(node for node, _ in providers if node["identity"]["principal"] == "hello-0")
        assert child["identity"]["name"] == deployment["node"]["name"] and child["identity"]["instance_id"], child
        assert child["identity"]["labels"] == deployment["node"]["labels"], child
        assert {requirement["id"] for requirement in child["requires"]} >= {"template-hello", "deployment-hello"}, child
        result = self.wait(lambda: self.call("hello.invoke", "/hello", data={"name": "Docker Mesh"}), lambda result: result == {"message": "Hello, Docker Mesh!"})
        self.call("deployment.reconcile", "/executor/deployments?id=hello", "DELETE")
        self.wait(lambda: self.providers("hello.invoke"), lambda providers: not providers)

    def security(self):
        self.request("/api/topology", expected=401, client=urllib.request.build_opener())
        marker = secrets.token_hex(24)
        self.call("secrets.resolve", "/secrets/put", "PUT", {"key": "acceptance", "value": marker})
        listing = self.call("secrets.resolve", "/secrets/list", data={})
        assert marker not in json.dumps(listing) and marker not in json.dumps(self.topology())
        self.call("secrets.resolve", "/secrets/resolve", data={"key": "acceptance"}, expected=403)
        for service, url in [("llm", "http://executor:8080/executor/containers"), ("llm", "http://knowledge:8080/secrets/resolve")]:
            arguments = ["exec", "-T", service, "wget", "-S", "-O", "/dev/null"]
            if "/secrets/" in url:
                arguments.append('--post-data={"key":"acceptance"}')
            result = self.compose(*arguments, url, check=False)
            assert result.returncode != 0 and "401" in result.stderr, result.stderr
        username, password = "reader-" + secrets.token_hex(5), secrets.token_urlsafe(20)
        self.request("/api/users", {"username": username, "password": password, "scopes": ["mesh.inspect", "llm.invoke", "secrets.read"]}, method="PUT", expected=201)
        admin, csrf = self.client, self.csrf
        try:
            self.client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            self.csrf = self.request("/api/auth/login", {"username": username, "password": password})["csrf"]
            self.call("container.manage", "/executor/containers", "GET", expected=403)
            self.call("llm.models.list", "/llm/config", "PUT", {"models": []}, expected=403)
            listing = self.call("secrets.resolve", "/secrets/list", data={"owner": "forged-owner"})
            assert listing["items"] == [], "browser user sees another user's secrets"
        finally:
            self.client, self.csrf = admin, csrf

    def relay(self):
        self.compose("--profile", "relay", "--profile", "remote-test", "up", "-d", "relay", "remote-model")
        remote = self.wait(lambda: self.providers("llm.invoke"), lambda items: any(node["identity"]["principal"] == "remote-model" for node, _ in items))
        node = next(node for node, _ in remote if node["identity"]["principal"] == "remote-model")
        assert all("/relay/forward/" in endpoint for endpoint in node["transport"]["advertise"]), node["transport"]
        marker = "relay-" + secrets.token_hex(6)
        result = self.invoke_mock("remote-model", marker)
        assert marker in result["message"]["content"], result
        self.compose("restart", "relay")
        result = self.invoke_mock("remote-model", marker)
        assert marker in result["message"]["content"], result

    def surface(self):
        state = self.topology()
        principals = {node["identity"]["principal"] for node in self.manifests()}
        assert {"agent", "store", "llm", "web"} <= principals, principals
        assert "orchestrator" not in principals
        assert any(node.get("requires") for node in self.manifests()), "no declared dependencies"
        assert "bindings" in state and "resolutions" in state

    def finish(self, keep):
        logs = self.compose("--profile", "executor", "--profile", "relay", "--profile", "remote-test", "logs", "--no-color", check=False)
        (self.output / "containers.log").write_text(logs.stdout + logs.stderr, encoding="utf-8")
        if not keep:
            self.compose("--profile", "executor", "--profile", "relay", "--profile", "remote-test", "--profile", "multi-provider", "down", check=False)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--project", default="whalemesh-acceptance")
    parser.add_argument("--base", help="Override automatically discovered dashboard port")
    parser.add_argument("--keep", action="store_true")
    args = parser.parse_args()
    test = Acceptance(args.project, args.base)
    try:
        test.check("A minimal chat without dashboard, orchestrator or executor", test.minimal)
        test.check("default deployment startup", test.start)
        test.check("A browser adapter chat without executor", lambda: test.chat("browser-" + secrets.token_hex(6)))
        test.check("B dynamic optional knowledge", test.dynamic_knowledge)
        test.check("C/D multiple providers and failover", test.multiple_and_failover)
        test.check("E managed Docker joins ordinary mesh", test.executor)
        run_extended(test)
        test.check("F secret and unauthenticated management isolation", test.security)
        test.check("multi-network outbound relay and restart", test.relay)
        test.check("G/H actual mesh browser surface", test.surface)
    finally:
        test.finish(args.keep)


if __name__ == "__main__":
    main()
