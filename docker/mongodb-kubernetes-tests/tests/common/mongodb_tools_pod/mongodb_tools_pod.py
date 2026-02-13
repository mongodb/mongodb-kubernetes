import tarfile
import tempfile

from kubernetes import client, config
from kubernetes.stream import stream
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

TOOLS_POD_NAME = "mongodb-tools-pod"
TOOLS_POD_IMAGE = "mongodb/mongodb-community-server:8.0-ubi9"


class ToolsPod:
    """A pod running MongoDB tools for executing commands like mongorestore inside the cluster."""

    def __init__(self, namespace: str):
        self.namespace = namespace
        self.pod_name = TOOLS_POD_NAME
        config.load_incluster_config()
        self.core_v1 = client.CoreV1Api()

    def run_command(self, cmd: list[str]):
        """Execute a command in the tools pod and return the output."""
        logger.debug(f"Running command in {self.pod_name}: {' '.join(cmd)}")
        resp = stream(
            self.core_v1.connect_get_namespaced_pod_exec,
            self.pod_name,
            self.namespace,
            command=cmd,
            stderr=True,
            stdin=False,
            stdout=True,
            tty=False,
        )
        logger.debug(f"Command output: {resp}")
        return resp

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
                        image=TOOLS_POD_IMAGE,
                        command=["/bin/bash", "-c"],
                        args=["sleep infinity"],
                    )
                ],
                restart_policy="Never",
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

        # Wait for pod to be ready
        from kubernetes.watch import Watch

        w = Watch()
        for event in w.stream(
            self.core_v1.list_namespaced_pod,
            namespace=self.namespace,
            label_selector="app=mongodb-tools",
            timeout_seconds=120,
        ):
            pod = event["object"]
            if pod.status.phase == "Running":
                # Check if container is ready
                if pod.status.container_statuses:
                    for container_status in pod.status.container_statuses:
                        if container_status.ready:
                            logger.info(f"{self.pod_name} is ready")
                            w.stop()
                            return
        raise TimeoutError(f"Timed out waiting for {self.pod_name} to be ready")


def get_tools_pod(namespace: str) -> ToolsPod:
    """Create and return a ready tools pod in the given namespace."""
    tools_pod = ToolsPod(namespace)
    tools_pod.run_pod_and_wait()
    return tools_pod
