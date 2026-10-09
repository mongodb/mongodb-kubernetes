import tarfile
import tempfile
from typing import Optional

from kubernetes import client, config
from kubernetes.stream import stream
from kubetester import get_pod_when_ready
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

TOOLS_POD_NAME = "mongodb-tools-pod"

# The registry the operator pulls database images from, the MONGODB_REPO_URL value of the e2e deployment.
MDB_IMAGE_REPOSITORY = "quay.io/mongodb"


def tools_pod_image(mdb_version: str) -> str:
    """Builds the tools pod server image at the given version, enterprise versions end with
    -ent, so 8.0.6-ent uses mongodb-enterprise-server:8.0.6-ubi9 and 8.3.4 uses
    mongodb-community-server:8.3.4-ubi9. The e2e matrix runs MongoDB 6.0.5 and above, ubi9
    images exist from 6.0.4."""
    image_name = "mongodb-enterprise-server" if mdb_version.endswith("-ent") else "mongodb-community-server"
    return f"{MDB_IMAGE_REPOSITORY}/{image_name}:{mdb_version.removesuffix('-ent')}-ubi9"


class ToolsPod:
    """A pod running MongoDB tools for executing commands like mongorestore inside the cluster."""

    def __init__(self, namespace: str, mdb_version: str, api_client: Optional[client.ApiClient] = None):
        self.namespace = namespace
        self.pod_name = TOOLS_POD_NAME
        self.image = tools_pod_image(mdb_version)
        self.api_client = api_client
        self.core_v1 = client.CoreV1Api(api_client=api_client)

    def run_command(self, cmd: list[str]):
        """Execute cmd in pod; raise RuntimeError on non-zero exit."""
        logger.debug(f"Running command in {self.pod_name}: {' '.join(cmd)}")
        resp = stream(
            self.core_v1.connect_get_namespaced_pod_exec,
            self.pod_name,
            self.namespace,
            container="mongodb-tools",
            command=cmd,
            stderr=True,
            stdin=False,
            stdout=True,
            tty=False,
            _preload_content=False,
        )
        stdout_chunks = []
        stderr_chunks = []
        while resp.is_open():
            resp.update(timeout=1)
            if resp.peek_stdout():
                stdout_chunks.append(resp.read_stdout())
            if resp.peek_stderr():
                stderr_chunks.append(resp.read_stderr())
        stdout = "".join(stdout_chunks)
        stderr = "".join(stderr_chunks)
        rc = resp.returncode
        resp.close()
        logger.debug(f"Command stdout: {stdout}")
        if stderr:
            logger.debug(f"Command stderr: {stderr}")
        if rc not in (0, None):
            raise RuntimeError(
                f"Command in {self.pod_name} exited rc={rc}: {' '.join(cmd)}\n" f"stderr: {stderr}\nstdout: {stdout}"
            )
        return stdout

    def copy_file_to_pod(self, src_path: str, dest_path: str):
        """Copy a file from the local filesystem to the tools pod."""
        logger.debug(f"Copying {src_path} to {self.pod_name}:{dest_path}")

        # Create a tar archive containing the file
        with tempfile.NamedTemporaryFile(suffix=".tar") as tar_file:
            with tarfile.open(tar_file.name, "w") as tar:
                tar.add(src_path, arcname=dest_path.split("/")[-1])

            tar_file.seek(0)
            tar_data = tar_file.read()

        # Extract the tar archive in the pod
        exec_command = ["tar", "xf", "-", "-C", "/".join(dest_path.split("/")[:-1]) or "/"]
        resp = stream(
            self.core_v1.connect_get_namespaced_pod_exec,
            self.pod_name,
            self.namespace,
            container="mongodb-tools",
            command=exec_command,
            stderr=True,
            stdin=True,
            stdout=True,
            tty=False,
            _preload_content=False,
        )

        # Send the tar data
        resp.write_stdin(tar_data)
        resp.close()
        logger.debug(f"File copied to {self.pod_name}:{dest_path}")

    def run_pod_and_wait(self):
        """Create the tools pod and wait for it to be ready."""
        pod_body = client.V1Pod(
            api_version="v1",
            kind="Pod",
            metadata=client.V1ObjectMeta(name=self.pod_name, labels={"app": "mongodb-tools"}),
            spec=client.V1PodSpec(
                containers=[
                    client.V1Container(
                        name="mongodb-tools",
                        image=self.image,
                        command=["/bin/bash", "-c"],
                        args=["sleep infinity"],
                        # mongosh writes its config under HOME, which is not writable by the pod user in some server images, and the warning lands in command output.
                        env=[client.V1EnvVar(name="HOME", value="/tmp")],
                        security_context=client.V1SecurityContext(
                            allow_privilege_escalation=False,
                            capabilities=client.V1Capabilities(drop=["ALL"]),
                        ),
                    )
                ],
                restart_policy="Never",
                security_context=client.V1PodSecurityContext(
                    run_as_non_root=True,
                    run_as_user=2000,
                    seccomp_profile=client.V1SeccompProfile(type="RuntimeDefault"),
                ),
            ),
        )

        try:
            self.core_v1.create_namespaced_pod(namespace=self.namespace, body=pod_body)
            logger.info(f"Created {self.pod_name} in namespace {self.namespace}")
        except client.exceptions.ApiException as e:
            if e.status == 409:
                logger.info(f"Pod {self.pod_name} already exists")
            else:
                raise

        pod = get_pod_when_ready(self.namespace, "app=mongodb-tools", api_client=self.api_client, default_retry=120)
        if pod is None:
            raise TimeoutError(f"Timed out waiting for {self.pod_name} to be ready")
        logger.info(f"{self.pod_name} is ready")


def get_tools_pod(namespace: str, mdb_version: str, api_client: Optional[client.ApiClient] = None) -> ToolsPod:
    """Create and return a ready tools pod in the given namespace, the image matches the
    given MongoDB version."""
    tools_pod = ToolsPod(namespace, mdb_version, api_client=api_client)
    tools_pod.run_pod_and_wait()
    return tools_pod
