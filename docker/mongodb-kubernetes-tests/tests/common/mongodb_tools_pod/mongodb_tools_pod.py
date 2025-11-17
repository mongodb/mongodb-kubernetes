import tarfile
import tempfile
from typing import Optional

from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubernetes.stream import stream
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class ToolsPod:
    namespace: str
    pod_name: str
    container_name: str
    api_client: Optional[client.ApiClient] = None

    def __init__(self, namespace: str, api_client: Optional[client.ApiClient] = None):
        self.namespace = namespace
        self.pod_name = "mongodb-tools-pod"
        self.container_name = "mongodb-tools"
        self.api_client = api_client

    def run_command(self, cmd: list[str]):

        logger.debug(f"Running command in pod {self.namespace}/{self.pod_name}: {" ".join(cmd)}")
        api_client = client.CoreV1Api(api_client=self.api_client)
        resp = stream(
            api_client.connect_get_namespaced_pod_exec,
            self.pod_name,
            self.namespace,
            container=self.container_name,
            command=cmd,
            stdout=True,
            stderr=True,
            _preload_content=False,
        )
        resp.run_forever(timeout=60)
        exit_code = resp.returncode
        output = resp.read_all()

        if exit_code != 0:
            raise RuntimeError(f"Command failed with exit code {exit_code}: {output}")

        return output

    def copy_file_to_pod(self, src_path: str, dest_path: str):
        api_client = client.CoreV1Api(api_client=self.api_client)
        try:
            exec_command = ["tar", "xvf", "-", "-C", "/"]
            resp = stream(
                api_client.connect_get_namespaced_pod_exec,
                self.pod_name,
                self.namespace,
                container=self.container_name,
                command=exec_command,
                stderr=True,
                stdin=True,
                stdout=True,
                tty=False,
                _preload_content=False,
            )

            stdout_output = []
            stderr_output = []

            with tempfile.TemporaryFile() as tar_buffer:
                with tarfile.open(fileobj=tar_buffer, mode="w") as tar:
                    tar.add(src_path, dest_path)

                tar_buffer.seek(0)
                commands = [tar_buffer.read()]

                while resp.is_open():
                    resp.update(timeout=1)
                    if resp.peek_stdout():
                        stdout = resp.read_stdout()
                        stdout_output.append(stdout)
                    if resp.peek_stderr():
                        stderr = resp.read_stderr()
                        stderr_output.append(stderr)
                    if commands:
                        c = commands.pop(0)
                        resp.write_stdin(c.decode())
                    else:
                        break

            resp.run_forever(timeout=5)
            exit_code = resp.returncode
            if exit_code is not None and exit_code != 0:
                output = "".join(stdout_output + stderr_output)
                raise RuntimeError(
                    f"Failed to copy file to pod: tar command exited with code {exit_code}\nOutput: {output}"
                )

        except ApiException as e:
            raise Exception(f"Failed to copy file to the pod: {e}")

    def run_pod_and_wait(self):
        from kubetester import get_pod_when_ready

        pod_exists = False
        try:
            client.CoreV1Api(api_client=self.api_client).read_namespaced_pod(self.pod_name, self.namespace)
            pod_exists = True
            logger.debug(f"{self.pod_name} already exists in namespace {self.namespace}")
        except Exception:
            pass

        if not pod_exists:
            pod_body = {
                "apiVersion": "v1",
                "kind": "Pod",
                "metadata": {
                    "name": self.pod_name,
                    "labels": {"app": self.pod_name},
                },
                "spec": {
                    "containers": [
                        {
                            "name": self.container_name,
                            "image": "mongodb/mongodb-community-server:8.0-ubi9",
                            "command": ["/bin/bash", "-c"],
                            "args": ["trap 'exit 0' SIGTERM; while true; do sleep 1; done"],
                        }
                    ],
                    "restartPolicy": "Never",
                },
            }
            client.CoreV1Api(api_client=self.api_client).create_namespaced_pod(self.namespace, pod_body)
            logger.info(f"Created {self.pod_name} in namespace {self.namespace}")

        logger.debug(f"Waiting for tools pod ({self.namespace}/{self.pod_name}) to become ready")
        get_pod_when_ready(self.namespace, f"app={self.pod_name}", default_retry=60)


def get_tools_pod(namespace: str) -> ToolsPod:
    tools_pod = ToolsPod(namespace)
    tools_pod.run_pod_and_wait()

    return tools_pod
