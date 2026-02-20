"""Test artifact data collection for E2E test summaries."""

import re
import sys
import xml.etree.ElementTree as ET
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

try:
    import yaml
except ImportError:
    print("Warning: PyYAML not available, YAML parsing will be limited", file=sys.stderr)
    yaml = None

from error_patterns import ERROR_PATTERNS, get_compiled_patterns


class TestSummaryGenerator:
    """Generates comprehensive test summary from E2E artifacts."""

    def __init__(self, logs_dir: Path):
        self.logs_dir = logs_dir
        self.data = {
            "meta": {},
            "test_run": {},
            "topology": {},
            "resources": {
                "pods": [],
                "statefulsets": [],
                "deployments": [],
                "services": [],
                "secrets": [],
                "configmaps": [],
                "pvcs": [],
                "custom_resources": {},
                "generic": {},  # slug -> {display_name, items: [...], source_files: [...]}
            },
            "resource_graph": {"edges": []},
            "diagnostics": {},  # cluster_label -> {file, cluster, namespace}
            "errors": [],
            "error_patterns": [],
            "timeline": [],
            "artifacts": {"files": [], "summary": {}},
        }
        self.error_id_counter = 0
        self.errors_by_pattern = defaultdict(list)
        self._compiled_patterns = get_compiled_patterns()

    def generate(self) -> Dict[str, Any]:
        """Generate complete test summary data."""
        print("Collecting test metadata...", file=sys.stderr)
        self._collect_metadata()

        print("Parsing test results...", file=sys.stderr)
        self._parse_test_results()

        print("Detecting topology...", file=sys.stderr)
        self._detect_topology()

        print("Building resource inventory...", file=sys.stderr)
        self._build_resource_inventory()

        print("Extracting errors from logs...", file=sys.stderr)
        self._extract_errors()

        print("Building timeline...", file=sys.stderr)
        self._build_timeline()

        print("Analyzing resource relationships...", file=sys.stderr)
        self._build_resource_graph()

        print("Cataloging artifacts...", file=sys.stderr)
        self._catalog_artifacts()

        print("Generating error patterns...", file=sys.stderr)
        self._generate_error_patterns()

        print("Embedding log tails...", file=sys.stderr)
        self._embed_log_tails()

        return self.data

    def _collect_metadata(self):
        """Collect test metadata."""
        self.data["meta"] = {
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "task_id": "[TASK_ID]",  # Filled by integration script
            "execution": 0,
            "test_type": "[TEST_TYPE]",  # Filled by integration script
        }

    def _parse_test_results(self):
        """Parse JUnit XML test results."""
        xml_report = self.logs_dir / "myreport.xml"
        if not xml_report.exists():
            self.data["test_run"] = {
                "status": "unknown",
                "tests": {"total": 0, "passed": 0, "failed": 0, "skipped": 0, "details": []},
            }
            return

        try:
            tree = ET.parse(xml_report)
            root = tree.getroot()
            testsuite = root.find(".//testsuite")

            if testsuite is not None:
                total = int(testsuite.get("tests", 0))
                failures = int(testsuite.get("failures", 0))
                errors = int(testsuite.get("errors", 0))
                skipped = int(testsuite.get("skipped", 0))
                passed = total - failures - errors - skipped
                duration = float(testsuite.get("time", 0))

                status = "passed" if (failures + errors) == 0 else "failed"

                test_details = []
                for testcase in root.findall(".//testcase"):
                    test_name = testcase.get("name", "")
                    test_duration = float(testcase.get("time", 0))
                    test_file = testcase.get("file", "")
                    test_line = testcase.get("line", "")

                    failure = testcase.find("failure")
                    error = testcase.find("error")

                    if failure is not None or error is not None:
                        elem = failure if failure is not None else error
                        error_msg = elem.get("message", "")
                        test_status = "failed"
                    else:
                        error_msg = None
                        test_status = "passed"

                    test_details.append(
                        {
                            "name": test_name,
                            "status": test_status,
                            "duration": test_duration,
                            "error_message": error_msg,
                            "file": test_file,
                            "line": test_line,
                        }
                    )

                self.data["test_run"] = {
                    "status": status,
                    "duration_seconds": duration,
                    "tests": {
                        "total": total,
                        "passed": passed,
                        "failed": failures + errors,
                        "skipped": skipped,
                        "details": test_details,
                    },
                }
        except Exception as e:
            print(f"Warning: Failed to parse test results: {e}", file=sys.stderr)
            self.data["test_run"] = {
                "status": "unknown",
                "tests": {"total": 0, "passed": 0, "failed": 0, "skipped": 0, "details": []},
            }

    def _detect_topology(self):
        """Detect test topology (single-cluster vs multi-cluster)."""
        # Look for diagnostics files with cluster prefixes
        diag_files = sorted(self.logs_dir.glob("*0_diagnostics.txt"))

        if len(diag_files) <= 1:
            # Single cluster
            self.data["topology"] = {
                "type": "single-cluster",
                "clusters": [{"context": "default", "namespaces": [], "role": "single"}],
            }
        else:
            # Multi-cluster: extract contexts from filenames
            clusters = []
            for diag_file in diag_files:
                parts = diag_file.name.split("_")
                if len(parts) > 1:
                    context = parts[0]
                    # Try to determine role from context name
                    role = "central" if "central" in context else "member"
                    clusters.append({"context": context, "namespaces": [], "role": role})

            self.data["topology"] = {"type": "multi-cluster", "clusters": clusters}

    def _build_resource_inventory(self):
        """Build inventory of Kubernetes resources from YAML dumps."""
        if yaml is None:
            print("Warning: Skipping resource inventory (PyYAML not available)", file=sys.stderr)
            return

        # Parse pod dumps
        self._parse_pods()
        self._parse_statefulsets()
        self._parse_deployments()
        self._parse_generic_resources()
        self._parse_diagnostics()

    def _parse_pods(self):
        """Parse pod information from z_pods.txt files."""
        if yaml is None:
            return

        pod_files = list(self.logs_dir.glob("*z_pods.txt"))
        for pod_file in pod_files:
            cluster, namespace = self._extract_cluster_namespace(pod_file.name)

            try:
                with open(pod_file, "r") as f:
                    content = f.read()

                    # Skip header lines (e.g., "----\nPods\n----")
                    # Find the first line that starts with "apiVersion:" or "items:"
                    lines = content.split("\n")
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(("apiVersion:", "items:")):
                            yaml_start = i
                            break

                    yaml_content = "\n".join(lines[yaml_start:])

                    try:
                        data = yaml.safe_load(yaml_content)
                        if not data:
                            continue

                        # Handle two formats:
                        # 1. List format: {apiVersion: v1, items: [...]}
                        # 2. Individual documents separated by ---
                        pods = []
                        if isinstance(data, dict) and "items" in data:
                            # List format
                            pods = data.get("items", [])
                        elif isinstance(data, dict) and data.get("kind") == "Pod":
                            # Single pod document
                            pods = [data]

                        for pod in pods:
                            if not pod or pod.get("kind") != "Pod":
                                continue

                            pod_info = self._extract_pod_info(pod, cluster, namespace, pod_file.name)
                            if pod_info:
                                self.data["resources"]["pods"].append(pod_info)
                    except yaml.YAMLError as e:
                        print(f"Warning: YAML parse error in {pod_file.name}: {e}", file=sys.stderr)
                        continue
            except Exception as e:
                print(f"Warning: Failed to parse {pod_file.name}: {e}", file=sys.stderr)

    def _extract_pod_info(self, pod: Dict, cluster: str, namespace: str, source_file: str) -> Optional[Dict]:
        """Extract relevant pod information."""
        try:
            metadata = pod.get("metadata", {})
            spec = pod.get("spec", {})
            status = pod.get("status", {})

            pod_name = metadata.get("name", "unknown")
            pod_id = f"{cluster}/{namespace}/{pod_name}"

            # Container information
            containers = []
            container_statuses = status.get("containerStatuses", [])
            for cs in container_statuses:
                container_name = cs.get("name", "unknown")
                state = cs.get("state", {})
                last_state = cs.get("lastState", {})

                # Determine current state
                current_state = "unknown"
                if "running" in state:
                    current_state = "running"
                elif "waiting" in state:
                    current_state = "waiting"
                elif "terminated" in state:
                    current_state = "terminated"

                exit_code = None
                if "terminated" in last_state:
                    exit_code = last_state["terminated"].get("exitCode")

                containers.append(
                    {
                        "name": container_name,
                        "state": current_state,
                        "ready": cs.get("ready", False),
                        "restarts": cs.get("restartCount", 0),
                        "last_state": "terminated" if last_state.get("terminated") else None,
                        "exit_code": exit_code,
                    }
                )

            # Owner reference
            owner_refs = metadata.get("ownerReferences", [])
            owner_ref = None
            if owner_refs:
                owner = owner_refs[0]
                owner_ref = f"{owner.get('kind')}/{owner.get('name')}"

            # Conditions
            conditions = []
            for cond in status.get("conditions", []):
                conditions.append(
                    {
                        "type": cond.get("type"),
                        "status": cond.get("status"),
                        "reason": cond.get("reason"),
                    }
                )

            # Find related files
            related_files = self._find_pod_files(cluster, namespace, pod_name)

            return {
                "id": pod_id,
                "name": pod_name,
                "namespace": namespace,
                "cluster": cluster,
                "phase": status.get("phase", "unknown"),
                "ready": self._calculate_ready_status(containers),
                "restarts": sum(c["restarts"] for c in containers),
                "owner_ref": owner_ref,
                "created": metadata.get("creationTimestamp", ""),
                "containers": containers,
                "conditions": conditions,
                "files": related_files,
            }
        except Exception as e:
            print(f"Warning: Failed to extract pod info: {e}", file=sys.stderr)
            return None

    def _calculate_ready_status(self, containers: List[Dict]) -> str:
        """Calculate ready status string (e.g., '2/3')."""
        if not containers:
            return "0/0"
        ready_count = sum(1 for c in containers if c.get("ready", False))
        return f"{ready_count}/{len(containers)}"

    def _build_file_prefix(self, cluster: str, namespace: str) -> str:
        """Build the file prefix matching dump_namespace convention."""
        if cluster != "default":
            return f"{cluster}__{namespace}_"
        else:
            return f"_{namespace}_"

    def _find_pod_files(self, cluster: str, namespace: str, pod_name: str) -> Dict[str, Any]:
        """Find all files related to a specific pod.

        From dump_diagnostic_information.sh, per pod we get:
          - {prefix}{pod}-pod-describe.txt          (kubectl describe)
          - {prefix}{pod}-agent-verbose.log          (automation agent verbose log)
          - {prefix}{pod}-agent.log                  (automation agent log)
          - {prefix}{pod}-agent-stderr.log           (agent stderr)
          - {prefix}{pod}-monitoring-agent-verbose.log
          - {prefix}{pod}-monitoring-agent.log
          - {prefix}{pod}-monitoring-agent-stdout.log
          - {prefix}{pod}-mongod-container.log       (mongod container stdout)
          - {prefix}{pod}-mongodb-agent-container.log (agent container stdout)
          - {prefix}{pod}-mongodb.log                (mongod file log)
          - {prefix}{pod}-launcher.log               (launcher script log)
          - {prefix}{pod}-readiness.log              (readiness probe log)
          - {prefix}{pod}-agent-health-status.json   (agent health)
          - {prefix}{pod}-cluster-config.json        (cluster config)
          - {prefix}{pod}-automation-mongod.conf     (mongod config)
          - {prefix}{pod}-{container}.log            (per-container stdout)
          - {prefix}{pod}-{container}-previous.log   (previous container stdout)
        """
        prefix = self._build_file_prefix(cluster, namespace)
        pod_prefix = f"{prefix}{pod_name}-"

        files = {
            "describe": None,
            "logs": [],
            "agent_logs": [],
            "config": [],
            "health": None,
        }

        for f in self.logs_dir.iterdir():
            if not f.is_file() or not f.name.startswith(pod_prefix):
                continue

            suffix = f.name[len(pod_prefix) :]

            if suffix == "pod-describe.txt":
                files["describe"] = f.name
            elif suffix == "agent-health-status.json":
                files["health"] = f.name
            elif suffix in ("cluster-config.json", "automation-mongod.conf"):
                files["config"].append(f.name)
            elif suffix.endswith(".log") and any(
                suffix.startswith(a)
                for a in (
                    "agent-verbose",
                    "agent.",
                    "agent-stderr",
                    "monitoring-agent",
                )
            ):
                files["agent_logs"].append(f.name)
            elif suffix.endswith(".log"):
                files["logs"].append(f.name)

        return files

    def _parse_statefulsets(self):
        """Parse StatefulSet information."""
        if yaml is None:
            return

        sts_files = list(self.logs_dir.glob("*z_statefulsets.txt"))
        for sts_file in sts_files:
            cluster, namespace = self._extract_cluster_namespace(sts_file.name)

            try:
                with open(sts_file, "r") as f:
                    content = f.read()

                    # Skip header lines
                    lines = content.split("\n")
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(("apiVersion:", "items:")):
                            yaml_start = i
                            break

                    yaml_content = "\n".join(lines[yaml_start:])

                    try:
                        data = yaml.safe_load(yaml_content)
                        if not data:
                            continue

                        # Handle list format or individual documents
                        statefulsets = []
                        if isinstance(data, dict) and "items" in data:
                            statefulsets = data.get("items", [])
                        elif isinstance(data, dict) and data.get("kind") == "StatefulSet":
                            statefulsets = [data]

                        for sts in statefulsets:
                            if not sts or sts.get("kind") != "StatefulSet":
                                continue

                            sts_info = self._extract_statefulset_info(sts, cluster, namespace, sts_file.name)
                            if sts_info:
                                self.data["resources"]["statefulsets"].append(sts_info)
                    except yaml.YAMLError as e:
                        print(f"Warning: YAML parse error in {sts_file.name}: {e}", file=sys.stderr)
                        continue
            except Exception as e:
                print(f"Warning: Failed to parse {sts_file.name}: {e}", file=sys.stderr)

    def _extract_statefulset_info(self, sts: Dict, cluster: str, namespace: str, source_file: str) -> Optional[Dict]:
        """Extract relevant StatefulSet information."""
        try:
            metadata = sts.get("metadata", {})
            spec = sts.get("spec", {})
            status = sts.get("status", {})

            sts_name = metadata.get("name", "unknown")
            sts_id = f"{cluster}/{namespace}/{sts_name}"

            owner_refs = metadata.get("ownerReferences", [])
            owner_ref = None
            if owner_refs:
                owner = owner_refs[0]
                owner_ref = f"{owner.get('kind')}/{owner.get('name')}"

            return {
                "id": sts_id,
                "name": sts_name,
                "namespace": namespace,
                "cluster": cluster,
                "desired_replicas": spec.get("replicas", 0),
                "current_replicas": status.get("currentReplicas", 0),
                "ready_replicas": status.get("readyReplicas", 0),
                "owner_ref": owner_ref,
                "created": metadata.get("creationTimestamp", ""),
                "selector": spec.get("selector", {}),
                "files": {"yaml": source_file},
            }
        except Exception as e:
            print(f"Warning: Failed to extract StatefulSet info: {e}", file=sys.stderr)
            return None

    def _parse_deployments(self):
        """Parse Deployment information."""
        if yaml is None:
            return

        deploy_files = list(self.logs_dir.glob("*z_deployments.txt"))
        for deploy_file in deploy_files:
            cluster, namespace = self._extract_cluster_namespace(deploy_file.name)

            try:
                with open(deploy_file, "r") as f:
                    content = f.read()

                    # Skip header lines
                    lines = content.split("\n")
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(("apiVersion:", "items:")):
                            yaml_start = i
                            break

                    yaml_content = "\n".join(lines[yaml_start:])

                    try:
                        data = yaml.safe_load(yaml_content)
                        if not data:
                            continue

                        # Handle list format or individual documents
                        deployments = []
                        if isinstance(data, dict) and "items" in data:
                            deployments = data.get("items", [])
                        elif isinstance(data, dict) and data.get("kind") == "Deployment":
                            deployments = [data]

                        for deploy in deployments:
                            if not deploy or deploy.get("kind") != "Deployment":
                                continue

                            deploy_info = self._extract_deployment_info(deploy, cluster, namespace, deploy_file.name)
                            if deploy_info:
                                self.data["resources"]["deployments"].append(deploy_info)
                    except yaml.YAMLError as e:
                        print(f"Warning: YAML parse error in {deploy_file.name}: {e}", file=sys.stderr)
                        continue
            except Exception as e:
                print(f"Warning: Failed to parse {deploy_file.name}: {e}", file=sys.stderr)

    def _extract_deployment_info(self, deploy: Dict, cluster: str, namespace: str, source_file: str) -> Optional[Dict]:
        """Extract relevant Deployment information."""
        try:
            metadata = deploy.get("metadata", {})
            spec = deploy.get("spec", {})
            status = deploy.get("status", {})

            deploy_name = metadata.get("name", "unknown")
            deploy_id = f"{cluster}/{namespace}/{deploy_name}"

            return {
                "id": deploy_id,
                "name": deploy_name,
                "namespace": namespace,
                "cluster": cluster,
                "desired_replicas": spec.get("replicas", 0),
                "current_replicas": status.get("replicas", 0),
                "ready_replicas": status.get("readyReplicas", 0),
                "created": metadata.get("creationTimestamp", ""),
                "files": {"yaml": source_file},
            }
        except Exception as e:
            print(f"Warning: Failed to extract Deployment info: {e}", file=sys.stderr)
            return None

    @staticmethod
    def _extract_resource_slug(filename: str) -> Optional[str]:
        """Extract the resource slug from a z_* filename.

        Split on '_z_', take the right side, strip extension.
        secret_ files -> 'secrets', *-pod-describe -> None (skip), else as-is.
        """
        if "_z_" not in filename:
            return None
        right = filename.split("_z_", 1)[1]
        # Strip .txt / .log extension
        right = re.sub(r"\.(txt|log)$", "", right)
        if right.startswith("secret_"):
            return "secrets"
        if "-pod-describe" in right:
            return None
        return right

    @staticmethod
    def _slug_to_display_name(slug: str) -> str:
        """Convert a resource slug to a human-readable display name."""
        acronyms = {"pvcs", "crds", "olm"}
        words = slug.split("_")
        parts = []
        for w in words:
            if w.lower() in acronyms:
                parts.append(w.upper())
            else:
                parts.append(w.capitalize())
        return " ".join(parts)

    def _parse_generic_resources(self):
        """Parse all z_* files into a generic resource map keyed by slug."""
        if yaml is None:
            print("Warning: Skipping generic resource parsing (PyYAML not available)", file=sys.stderr)
            return

        z_files = sorted(self.logs_dir.glob("*_z_*"))
        # Group files by slug
        slug_files = defaultdict(list)
        for f in z_files:
            if not f.is_file():
                continue
            slug = self._extract_resource_slug(f.name)
            if slug is None:
                continue
            slug_files[slug].append(f)

        for slug, files in slug_files.items():
            display_name = self._slug_to_display_name(slug)
            items = []
            source_files = []

            for f in files:
                cluster, namespace = self._extract_cluster_namespace(f.name)
                source_files.append(
                    {
                        "name": f.name,
                        "cluster": cluster,
                        "namespace": namespace,
                    }
                )

                if slug == "secrets":
                    # Secret files: extract name from filename, don't YAML-parse
                    right = f.name.split("_z_secret_", 1)
                    if len(right) == 2:
                        secret_name = re.sub(r"\.(txt|log)$", "", right[1])
                    else:
                        secret_name = f.name
                    items.append(
                        {
                            "name": secret_name,
                            "namespace": namespace,
                            "cluster": cluster,
                            "kind": "Secret",
                            "source_file": f.name,
                        }
                    )
                    continue

                # Try YAML parse
                try:
                    with open(f, "r") as fh:
                        content = fh.read()

                    # Skip header lines (e.g. "----\nPods\n----")
                    lines = content.split("\n")
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(("apiVersion:", "items:", "kind:")):
                            yaml_start = i
                            break

                    yaml_content = "\n".join(lines[yaml_start:])
                    data = yaml.safe_load(yaml_content)

                    if isinstance(data, dict) and "items" in data:
                        for item in data["items"] or []:
                            if not isinstance(item, dict):
                                continue
                            metadata = item.get("metadata", {})
                            items.append(
                                {
                                    "name": metadata.get("name", "unknown"),
                                    "namespace": metadata.get("namespace", namespace),
                                    "cluster": cluster,
                                    "kind": item.get("kind", ""),
                                    "api_version": item.get("apiVersion", ""),
                                    "created": metadata.get("creationTimestamp", ""),
                                    "source_file": f.name,
                                }
                            )
                    elif isinstance(data, dict) and "metadata" in data:
                        metadata = data.get("metadata", {})
                        items.append(
                            {
                                "name": metadata.get("name", "unknown"),
                                "namespace": metadata.get("namespace", namespace),
                                "cluster": cluster,
                                "kind": data.get("kind", ""),
                                "api_version": data.get("apiVersion", ""),
                                "created": metadata.get("creationTimestamp", ""),
                                "source_file": f.name,
                            }
                        )
                    # else: raw/describe file — no items, but source_files still tracked
                except Exception as e:
                    print(f"Warning: Could not parse {f.name}: {e}", file=sys.stderr)

            self.data["resources"]["generic"][slug] = {
                "display_name": display_name,
                "items": items,
                "source_files": source_files,
            }

    def _parse_diagnostics(self):
        """Parse diagnostics files and store references."""
        diag_files = sorted(self.logs_dir.glob("*0_diagnostics.txt"))
        for f in diag_files:
            cluster, namespace = self._extract_cluster_namespace(f.name)
            label = f"{cluster}/{namespace}" if cluster != "default" else namespace
            self.data["diagnostics"][label] = {
                "file": f.name,
                "cluster": cluster,
                "namespace": namespace,
            }

    def _extract_cluster_namespace(self, filename: str) -> Tuple[str, str]:
        """Extract cluster and namespace from filename.

        File naming comes from dump_diagnostic_information.sh:
          dump_namespace builds prefix as "${context_prefix}_${namespace}_"

        Multi-cluster: context_prefix = "${context}_" -> files are "{context}__{namespace}_{rest}"
          e.g. kind-e2e-cluster-1__a-1770815153-mxfvgwckn0z_z_pods.txt

        Single-cluster: context_prefix = "" -> files are "_{namespace}_{rest}" or "{namespace}_{rest}"
          e.g. _a-1770815153-mxfvgwckn0z_z_pods.txt

        The '__' double underscore is the key separator between context and namespace.
        K8s namespaces never contain underscores, so the first '_' after the namespace
        separates it from the rest of the filename.
        """
        # Multi-cluster: split on '__'
        if "__" in filename:
            context, rest = filename.split("__", 1)
            # Namespace ends at first '_' (K8s namespaces have no underscores)
            if "_" in rest:
                namespace, _ = rest.split("_", 1)
            else:
                namespace = rest
            return context, namespace

        # Single-cluster: may start with '_' from prefix="" + "_${namespace}_"
        name = filename.lstrip("_")
        if "_" in name:
            namespace, _ = name.split("_", 1)
            return "default", namespace

        return "default", "default"

    def _extract_errors(self):
        """Extract errors from log files."""
        log_files = list(self.logs_dir.glob("*.log")) + list(self.logs_dir.glob("*-container.log"))

        for log_file in log_files:
            self._scan_log_for_errors(log_file)

    def _scan_log_for_errors(self, log_file: Path, max_errors_per_file: int = 50):
        """Scan a log file for error patterns."""
        # Extract resource info from filename
        resource, container = self._parse_log_filename(log_file.name)

        try:
            with open(log_file, "r", errors="ignore") as f:
                error_count = 0
                for line_no, line in enumerate(f, 1):
                    if error_count >= max_errors_per_file:
                        break

                    # Check each pattern using pre-compiled regexes
                    for pattern_name, compiled_regex, pattern_info in self._compiled_patterns:
                        if compiled_regex.search(line):
                            error_id = f"err-{self.error_id_counter:04d}"
                            self.error_id_counter += 1

                            # Try to extract timestamp
                            timestamp = self._extract_timestamp(line)

                            error_entry = {
                                "id": error_id,
                                "timestamp": timestamp,
                                "severity": pattern_info["severity"],
                                "pattern": pattern_name,
                                "source": {
                                    "resource": resource,
                                    "container": container,
                                    "file": log_file.name,
                                    "line": line_no,
                                },
                                "message": line.strip()[:200],  # Truncate long lines
                                "context": line.strip()[:500],
                            }

                            self.data["errors"].append(error_entry)
                            self.errors_by_pattern[pattern_name].append(error_id)
                            error_count += 1
                            break  # Only match one pattern per line
        except Exception as e:
            print(f"Warning: Failed to scan {log_file.name}: {e}", file=sys.stderr)

    def _parse_log_filename(self, filename: str) -> Tuple[str, str]:
        """Parse log filename to extract resource and container info.

        Uses the same '__' based parsing as _extract_cluster_namespace.
        After extracting cluster/namespace, the rest is {pod}-{suffix}.log
        """
        cluster, namespace = self._extract_cluster_namespace(filename)
        prefix = self._build_file_prefix(cluster, namespace)

        # Strip prefix and extension to get pod-suffix
        rest = filename
        if filename.startswith(prefix):
            rest = filename[len(prefix) :]
        rest = re.sub(r"\.(log|txt|json|conf)$", "", rest)

        # Known suffixes from dump_pod_logs in dump_diagnostic_information.sh
        container = "unknown"
        pod_name = rest

        known_suffixes = [
            "-agent-verbose",
            "-agent-stderr",
            "-agent",
            "-monitoring-agent-verbose",
            "-monitoring-agent-stdout",
            "-monitoring-agent",
            "-mongodb-agent-container",
            "-mongod-container",
            "-mongodb-enterprise-database",
            "-mongodb",
            "-launcher",
            "-readiness",
            "-istio-proxy",
            "-keepalive",
        ]

        for suffix in sorted(known_suffixes, key=len, reverse=True):
            if rest.endswith(suffix):
                pod_name = rest[: -len(suffix)]
                container = suffix.lstrip("-")
                break

        resource = f"Pod/{cluster}/{namespace}/{pod_name}"
        return resource, container

    def _extract_timestamp(self, line: str) -> Optional[str]:
        """Try to extract ISO timestamp from log line."""
        # Common patterns:
        # 2025-02-11T07:23:45Z
        # 2025-02-11 07:23:45
        patterns = [
            r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?)",
            r"(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})",
        ]

        for pattern in patterns:
            match = re.search(pattern, line)
            if match:
                timestamp_str = match.group(1)
                # Normalize to ISO format
                if "T" not in timestamp_str:
                    timestamp_str = timestamp_str.replace(" ", "T") + "Z"
                elif not timestamp_str.endswith("Z"):
                    timestamp_str += "Z"
                return timestamp_str

        return None

    def _build_timeline(self):
        """Build timeline from events and log errors."""
        # Parse Kubernetes events
        self._parse_k8s_events()

        # Add log errors to timeline (already extracted)
        for error in self.data["errors"]:
            if error.get("timestamp"):
                self.data["timeline"].append(
                    {
                        "timestamp": error["timestamp"],
                        "type": "log_error",
                        "severity": error["severity"],
                        "resource": error["source"]["resource"],
                        "message": error["message"],
                        "error_id": error["id"],
                    }
                )

        # Sort timeline by timestamp (handle None values)
        self.data["timeline"].sort(key=lambda x: x.get("timestamp") or "")

    def _parse_k8s_events(self):
        """Parse Kubernetes events from events_detailed.yaml files.

        The dump script (dump_events) produces two files per namespace:
          - events.txt: kubectl get events (relative timestamps like '14m', useless for sorting)
          - events_detailed.yaml: kubectl get events -o yaml (full structured YAML with real timestamps)

        We parse events_detailed.yaml for real timestamps and structured data.
        """
        if yaml is None:
            print("Warning: Skipping event parsing (PyYAML not available)", file=sys.stderr)
            return

        event_files = list(self.logs_dir.glob("*events_detailed.yaml"))

        for event_file in event_files:
            cluster, namespace = self._extract_cluster_namespace(event_file.name)

            try:
                with open(event_file, "r") as f:
                    data = yaml.safe_load(f)

                if not data or "items" not in data:
                    continue

                for event in data["items"]:
                    if not event or event.get("kind") != "Event":
                        continue

                    # Use lastTimestamp (most recent occurrence) or firstTimestamp
                    timestamp = event.get("lastTimestamp") or event.get("firstTimestamp")
                    event_type = event.get("type", "Normal")  # Normal or Warning
                    reason = event.get("reason", "")
                    message = event.get("message", "")
                    count = event.get("count", 1)

                    # Extract the involved object (e.g., Pod/my-pod-0)
                    involved = event.get("involvedObject", {})
                    obj_kind = involved.get("kind", "")
                    obj_name = involved.get("name", "")
                    resource = f"{obj_kind}/{obj_name}" if obj_kind else "unknown"

                    severity = "warning" if event_type == "Warning" else ""

                    self.data["timeline"].append(
                        {
                            "timestamp": timestamp,
                            "type": "k8s_event",
                            "event_type": event_type,
                            "reason": reason,
                            "resource": resource,
                            "message": message,
                            "count": count,
                            "cluster": cluster,
                            "namespace": namespace,
                            "severity": severity,
                        }
                    )

            except Exception as e:
                print(f"Warning: Failed to parse events from {event_file.name}: {e}", file=sys.stderr)

    def _build_resource_graph(self):
        """Build resource relationship graph."""
        # Build ownership edges
        for pod in self.data["resources"]["pods"]:
            if pod.get("owner_ref"):
                self.data["resource_graph"]["edges"].append(
                    {
                        "from": f"Pod/{pod['id']}",
                        "to": f"{pod['owner_ref']}/{pod['cluster']}/{pod['namespace']}",
                        "relationship": "owned_by",
                    }
                )

        for sts in self.data["resources"]["statefulsets"]:
            if sts.get("owner_ref"):
                self.data["resource_graph"]["edges"].append(
                    {
                        "from": f"StatefulSet/{sts['id']}",
                        "to": f"{sts['owner_ref']}/{sts['cluster']}/{sts['namespace']}",
                        "relationship": "owned_by",
                    }
                )

    def _catalog_artifacts(self):
        """Catalog all artifact files, grouped by cluster/namespace."""
        all_files = sorted(self.logs_dir.glob("*"))
        total_size = 0

        by_type = defaultdict(int)
        by_cluster = defaultdict(lambda: defaultdict(list))

        for file_path in all_files:
            if not file_path.is_file():
                continue

            size = file_path.stat().st_size
            total_size += size

            file_type = self._classify_file_type(file_path.name)
            by_type[file_type] += 1

            cluster, namespace = self._extract_cluster_namespace(file_path.name)

            file_entry = {
                "name": file_path.name,
                "type": file_type,
                "size_bytes": size,
                "cluster": cluster,
                "namespace": namespace,
            }

            self.data["artifacts"]["files"].append(file_entry)
            by_cluster[cluster][namespace].append(file_entry)

        file_count = sum(1 for f in all_files if f.is_file())
        self.data["artifacts"]["summary"] = {
            "total_files": file_count,
            "total_size_bytes": total_size,
            "by_type": dict(by_type),
        }
        self.data["artifacts"]["by_cluster"] = {
            cluster: {ns: files for ns, files in namespaces.items()} for cluster, namespaces in by_cluster.items()
        }

    def _classify_file_type(self, filename: str) -> str:
        """Classify file type from filename.

        The 'z_' prefix appears after the namespace prefix, not at the start.
        e.g., kind-e2e-cluster-1__ns-12345_z_pods.txt
        """
        if filename.endswith(".log"):
            return "log"
        elif "pod-describe" in filename:
            return "describe"
        elif "_z_" in filename and filename.endswith(".txt"):
            return "resource_dump"
        elif "_0_diagnostics.txt" in filename:
            return "diagnostics"
        elif "events_detailed.yaml" in filename:
            return "events"
        elif "events.txt" in filename:
            return "events"
        elif "agent-health-status.json" in filename:
            return "health"
        elif "automation-mongod.conf" in filename:
            return "config"
        elif "cluster-config.json" in filename:
            return "config"
        elif filename.endswith(".xml"):
            return "test_results"
        elif filename.endswith(".json"):
            return "json"
        elif filename.endswith(".yaml"):
            return "yaml"
        else:
            return "other"

    def _embed_log_tails(self, tail_lines: int = 500, max_total_bytes: int = 20 * 1024 * 1024):
        """Embed file content for inline viewing in the modal.

        For log files: embeds the last N lines (most recent activity).
        For structured files (YAML, JSON, conf, txt dumps): embeds from the beginning
        since these are not append-only streams.
        """
        self.data["log_tails"] = {}
        total_bytes = 0

        viewable_extensions = {".log", ".txt", ".json", ".conf", ".yaml", ".xml"}
        skip_patterns = ["test-summary"]

        # Structured files should be read from the beginning, not tailed
        head_extensions = {".yaml", ".json", ".conf", ".xml"}
        head_suffixes = {"_describe.txt", "_diagnostics.txt"}

        for file_path in sorted(self.logs_dir.iterdir()):
            if not file_path.is_file():
                continue
            if file_path.suffix not in viewable_extensions:
                continue
            if any(pat in file_path.name for pat in skip_patterns):
                continue
            if file_path.stat().st_size == 0:
                continue

            try:
                with open(file_path, "r", errors="replace") as f:
                    lines = f.readlines()

                # Structured files: read from beginning (head)
                # Log files: read from end (tail)
                use_head = (
                    file_path.suffix in head_extensions
                    or "_z_" in file_path.name
                    or any(file_path.name.endswith(s) for s in head_suffixes)
                )

                if use_head:
                    shown = lines[:tail_lines]
                    truncated = len(lines) > tail_lines
                else:
                    shown = lines[-tail_lines:]
                    truncated = len(lines) > tail_lines

                content = "".join(shown)

                # Cap total embedded size
                content_bytes = len(content.encode("utf-8", errors="replace"))
                if total_bytes + content_bytes > max_total_bytes:
                    print(f"  Skipping {file_path.name} (total size cap reached)", file=sys.stderr)
                    continue

                total_bytes += content_bytes
                self.data["log_tails"][file_path.name] = {
                    "content": content,
                    "total_lines": len(lines),
                    "shown_lines": len(shown),
                    "truncated": truncated,
                    "mode": "head" if use_head else "tail",
                }
            except Exception as e:
                print(f"  Warning: Could not read {file_path.name}: {e}", file=sys.stderr)

        print(f"  Embedded {len(self.data['log_tails'])} files ({total_bytes / 1024:.0f} KB)", file=sys.stderr)

    def _generate_error_patterns(self):
        """Generate error pattern summary."""
        for pattern_name, error_ids in self.errors_by_pattern.items():
            if not error_ids:
                continue

            pattern_info = ERROR_PATTERNS.get(pattern_name, {})

            # Get affected resources
            affected_resources = set()
            first_timestamp = None
            last_timestamp = None

            for error_id in error_ids:
                error = next((e for e in self.data["errors"] if e["id"] == error_id), None)
                if error:
                    affected_resources.add(error["source"]["resource"])
                    if error.get("timestamp"):
                        if first_timestamp is None or error["timestamp"] < first_timestamp:
                            first_timestamp = error["timestamp"]
                        if last_timestamp is None or error["timestamp"] > last_timestamp:
                            last_timestamp = error["timestamp"]

            self.data["error_patterns"].append(
                {
                    "pattern": pattern_name,
                    "count": len(error_ids),
                    "description": pattern_info.get("description", "Unknown pattern"),
                    "severity": pattern_info.get("severity", "unknown"),
                    "affected_resources": sorted(list(affected_resources)),
                    "error_ids": error_ids[:10],  # Limit to first 10
                    "first_occurrence": first_timestamp,
                    "last_occurrence": last_timestamp,
                }
            )

        # Sort by count (most common first)
        self.data["error_patterns"].sort(key=lambda x: x["count"], reverse=True)
