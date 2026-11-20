#!/usr/bin/env python3
"""Run with: python3 scripts/test/integration/test_mongodb_proxy.py

Requires Go, Python 3, and Docker with linux/amd64 support. The runner downloads
the Agent (about 48 MB). Docker supplies MongoDB. No Kubernetes or Ops Manager.
"""

import hashlib
import io
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request
from pathlib import Path

IMAGE = (
    "quay.io/mongodb/mongodb-enterprise-server:8.0.4-ubi9"
    "@sha256:3fb56fd5ef277019ac9559a3d93690db9225eebd0e6995504606ffe942ef5032"
)
# Evergreen patch 9338 builds the Agent from 40c168b57c. The archive expires on 2027-11-21.
AGENT_ARCHIVE = "mongodb-mms-automation-agent-110.0.0.9459-1.rhel8_x86_64"
AGENT_URL = (
    "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/"
    f"builds/patches/6b000f22d8b4a000073aa334/automation-agent/local/{AGENT_ARCHIVE}.tar.gz"
)
AGENT_SHA256 = "1601b1aa3a8240193370f98fde60795dd4a5c3b1369423f6474d2a76daff3449"


def download_agent(agent):
    with urllib.request.urlopen(AGENT_URL, timeout=60) as response:
        archive = response.read()
    if hashlib.sha256(archive).hexdigest() != AGENT_SHA256:
        raise RuntimeError("Agent archive does not match its pinned SHA-256")
    with tarfile.open(fileobj=io.BytesIO(archive)) as package:
        with package.extractfile(f"{AGENT_ARCHIVE}/mongodb-mms-automation-agent") as source:
            with agent.open("wb") as output:
                shutil.copyfileobj(source, output)
    agent.chmod(0o755)


def run_in_docker():
    for tool in ("docker", "go"):
        if not shutil.which(tool):
            sys.exit(f"Install {tool} to run the mongodb-proxy integration test.")
    script = Path(__file__).resolve()
    with tempfile.TemporaryDirectory(prefix="mongodb-proxy-build-") as directory:
        build = Path(directory)
        subprocess.run(
            ["go", "build", "-o", str(build / "mongodb-proxy"), "./cmd/mongodb-proxy"],
            cwd=script.parents[3],
            env={**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"},
            check=True,
        )
        download_agent(build / "agent")
        shutil.copyfile(script, build / script.name)
        build.chmod(0o755)  # The image runs as the non-root mongod user.
        return subprocess.call(
            [
                "docker",
                "run",
                "--rm",
                "--init",
                "--platform",
                "linux/amd64",
                "--network",
                "none",
                "--volume",
                f"{build}:/smoke:ro",
                "--entrypoint",
                "python3",
                IMAGE,
                f"/smoke/{script.name}",
                "--in-container",
            ],
        )


def request(sock, path, body=None, expected=200):
    # Give POST time to report the result of a stop with a 10-second grace period.
    timeout = "2" if body is None else "15"
    command = ["curl", "-sS", "--max-time", timeout, "--unix-socket", str(sock), "-w", "\n%{http_code}"]
    if body is not None:
        command += ["-H", "Content-Type: application/json", "--data", json.dumps(body)]
    command.append(f"http://localhost/v1/{path}")
    output = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    response, status = output.rsplit("\n", 1)
    assert int(status) == expected, f"{path}: expected HTTP {expected}, got {status}: {response}"
    return json.loads(response)


def wait_for(description, probe, *processes, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        for process in processes:
            if process.poll() is not None:
                raise RuntimeError(f"{process.args[0]} exited with code {process.returncode} waiting for {description}")
        try:
            result = probe()
            if result:
                return result
        except (OSError, subprocess.CalledProcessError):
            pass
        time.sleep(0.2)
    raise TimeoutError(f"Timed out waiting for {description}")


def mongo_eval(js, proxy, agent):
    def connect():
        with socket.create_connection(("127.0.0.1", 27017), timeout=1):
            return True

    wait_for("mongod to listen", connect, proxy, agent, timeout=30)
    subprocess.run(
        [
            "mongosh",
            "mongodb://127.0.0.1:27017/smoke?serverSelectionTimeoutMS=5000",
            "--norc",
            "--quiet",
            "--eval",
            'if (db.runCommand({ping: 1}).ok !== 1) throw new Error("ping failed"); ' + js,
        ],
        timeout=15,
        check=True,
    )


def write_cluster_config(work):
    data = work / "data"
    data.mkdir()
    version = "8.0.4"
    config = work / "cluster-config.json"
    config.write_text(
        json.dumps(
            {
                "version": 1,
                "auth": {"disabled": True},
                "options": {"downloadBase": str(work)},
                # The Agent resolves the version key even when the image supplies mongod.
                "mongoDbVersions": [
                    {
                        "name": version,
                        "builds": [
                            {
                                "architecture": "amd64",
                                "bits": 64,
                                "platform": "linux",
                                "flavor": "rhel",
                                "gitVersion": "bc35ab4305d9920d9d0491c1c9ef9b72383d31f9",
                                "modules": ["enterprise"],
                                "url": f"https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise-rhel93-{version}.tgz",
                            }
                        ],
                    }
                ],
                "processes": [
                    {
                        "name": "smoke",
                        "hostname": "127.0.0.1",
                        "processType": "mongod",
                        "version": version,
                        "featureCompatibilityVersion": "8.0",
                        "args2_6": {
                            "net": {"bindIp": "127.0.0.1", "port": 27017},
                            "storage": {"dbPath": str(data)},
                            "systemLog": {"destination": "file", "path": str(work / "mongod.log"), "logAppend": True},
                        },
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    return config


def launch(work, name, command):
    with (work / f"{name}.log").open("w") as log:
        return subprocess.Popen(command, cwd=work, stdout=log, stderr=subprocess.STDOUT)


def exercise_lifecycle(work):
    sock = work / "proxy.sock"
    config = write_cluster_config(work)
    address = f"unix://{sock}"
    proxy = launch(
        work,
        "proxy",
        [
            "/smoke/mongodb-proxy",
            "-listen",
            address,
            "-bin-dir",
            "/usr/bin",
            "-config-dir",
            str(work),
        ],
    )

    def running_after(previous_id=None):
        status = request(sock, "process")
        if status["state"] == "running" and status["executionId"] != previous_id:
            return status
        return None

    try:
        initial = wait_for("proxy socket", lambda: request(sock, "process"), proxy, timeout=10)
        assert initial["state"] == "idle", initial

        agent = launch(
            work,
            "agent",
            [
                "/smoke/agent",
                "-noDaemonize",
                "-operatorMode",
                "-overrideLocalHost=127.0.0.1",
                f"-cluster={config}",
                f"-mongodProxyAddress={address}",
                "-binariesFixedPath=/usr/bin",
            ],
        )
        # Includes the Agent planning interval and startup on emulated amd64.
        first = wait_for("Agent to start mongod", running_after, proxy, agent, timeout=60)
        assert first["role"] == "main" and first["executionId"], first
        mongo_eval('db.smoke.insertOne({_id: "before-crash"}, {writeConcern: {j: true}})', proxy, agent)

        os.kill(first["pid"], signal.SIGKILL)
        second = wait_for(
            "Agent to recover mongod", lambda: running_after(first["executionId"]), proxy, agent, timeout=60
        )
        assert second["role"] == "main" and second["executionId"], second
        stale_stop = {"role": "main", "executionId": first["executionId"], "timeoutSeconds": 10}
        rejected = request(sock, "process/stop", stale_stop, expected=409)
        assert rejected["code"] == "execution_mismatch", rejected
        mongo_eval('if (!db.smoke.findOne({_id: "before-crash"})) throw new Error("document missing")', proxy, agent)
        assert request(sock, "process")["executionId"] == second["executionId"]
        print("PASS: Agent recovered mongod through the proxy, retained data, and rejected a stale stop request")
    except Exception:
        for path in ("process", "events"):
            try:
                print(f"/v1/{path}: {request(sock, path)}", file=sys.stderr)
            except (AssertionError, OSError, ValueError, subprocess.CalledProcessError):
                pass
        for log in sorted(work.glob("*.log")):
            with log.open("rb") as stream:
                stream.seek(max(0, log.stat().st_size - 8192))
                print(f"{log.name}:\n{stream.read().decode(errors='replace')}", file=sys.stderr)
        raise
    # ponytail: the throwaway container owns process and filesystem cleanup.


if __name__ == "__main__":
    if sys.argv[1:] == ["--in-container"]:
        exercise_lifecycle(Path(tempfile.mkdtemp(prefix="mdb-proxy-")))
    elif not sys.argv[1:]:
        sys.exit(run_in_docker())
    else:
        sys.exit(__doc__)
