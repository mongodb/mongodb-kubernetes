from __future__ import annotations

import time
from typing import Optional

from kubeobject import CustomObject
from kubernetes import client
from kubetester.mongodb_common import MongoDBCommon
from kubetester.mongodb_utils_state import in_desired_state
from kubetester.phase import Phase
from opentelemetry import trace
from tests import test_logger

logger = test_logger.get_test_logger(__name__)
TRACER = trace.get_tracer("evergreen-agent")

VOYAGE_GROUP = "ai.mongodb.com"
VOYAGE_VERSION = "v1"


class _VoyageResource(CustomObject, MongoDBCommon):
    KIND: str
    PLURAL: str

    def __init__(self, name: str, namespace: str, api_client: Optional[client.ApiClient] = None):
        super().__init__(
            name,
            namespace,
            kind=self.KIND,
            plural=self.PLURAL,
            group=VOYAGE_GROUP,
            version=VOYAGE_VERSION,
            api_client=api_client,
        )

    def __repr__(self):
        return f"{self.KIND}({self.name!r}, {self.namespace!r})"

    def get_status(self) -> dict:
        return self.backing_obj.get("status") or {}

    def get_status_phase(self) -> Optional[Phase]:
        phase = self.get_status().get("phase")
        return Phase[phase] if phase else None

    def get_status_message(self) -> Optional[str]:
        return self.get_status().get("message")

    def get_status_observed_generation(self) -> Optional[int]:
        return self.get_status().get("observedGeneration")

    @TRACER.start_as_current_span("assert_reaches_phase")
    def assert_reaches_phase(
        self, phase: Phase, msg_regexp: Optional[str] = None, timeout: Optional[int] = None, ignore_errors=False
    ):
        start_time = time.time()
        self.wait_for(
            lambda s: in_desired_state(
                current_state=s.get_status_phase(),
                desired_state=phase,
                current_generation=s.get_generation(),
                observed_generation=s.get_status_observed_generation(),
                current_message=s.get_status_message(),
                msg_regexp=msg_regexp,
                ignore_errors=ignore_errors,
            ),
            timeout,
            should_raise=True,
            name=f"{self.KIND.lower()}_reaches_{phase.name.lower()}",
        )
        logger.debug(f"{self} reached phase {phase.name} in {time.time() - start_time:.1f}s")


class VoyageAI(_VoyageResource):
    KIND = "VoyageAI"
    PLURAL = "voyageais"

    def service_name(self) -> str:
        return f"{self.name}-svc"


class VoyageRouter(_VoyageResource):
    KIND = "VoyageRouter"
    PLURAL = "voyagerouters"
    CONFIG_HASH_ANNOTATION = "mongodb.com/envoy-config-hash"

    def deployment_name(self) -> str:
        return f"{self.name}-voyage-router"

    def service_name(self) -> str:
        return f"{self.name}-voyage-router-svc"

    def configmap_name(self) -> str:
        return f"{self.name}-voyage-router-config"

    def read_deployment(self) -> client.V1Deployment:
        return client.AppsV1Api().read_namespaced_deployment(self.deployment_name(), self.namespace)

    def read_service(self) -> client.V1Service:
        return client.CoreV1Api().read_namespaced_service(self.service_name(), self.namespace)

    def read_configmap(self) -> client.V1ConfigMap:
        return client.CoreV1Api().read_namespaced_config_map(self.configmap_name(), self.namespace)

    def config_hash(self) -> str:
        return self.read_deployment().spec.template.metadata.annotations[self.CONFIG_HASH_ANNOTATION]

    def managed_labels(self) -> dict[str, str]:
        return {
            "app.kubernetes.io/name": "voyage-router",
            "app.kubernetes.io/instance": self.name,
            "app.kubernetes.io/managed-by": "mongodb-kubernetes-operator",
        }

    def pod_labels(self) -> dict[str, str]:
        return {
            "app": self.deployment_name(),
            "app.kubernetes.io/name": "voyage-router",
            "app.kubernetes.io/instance": self.name,
        }

    def pod_label_selector(self) -> str:
        return ",".join(f"{key}={value}" for key, value in self.pod_labels().items())

    def get_condition(self, condition_type: str) -> Optional[dict]:
        for condition in self.get_status().get("conditions") or []:
            if condition.get("type") == condition_type:
                return condition
        return None
