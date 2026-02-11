#!/usr/bin/env python3
"""Generate comprehensive HTML test summary with embedded JSON for E2E test artifacts.

This tool processes E2E test artifacts (logs, YAML dumps, events) and generates a single
HTML file containing both structured JSON data (for AI agents) and an interactive UI (for humans).
"""

import argparse
import sys
import json
import re
from pathlib import Path
from html import escape
from datetime import datetime, timezone
from typing import Dict, List, Any, Optional, Tuple
from collections import defaultdict
import xml.etree.ElementTree as ET

try:
    import yaml
except ImportError:
    print("Warning: PyYAML not available, YAML parsing will be limited", file=sys.stderr)
    yaml = None


# Error patterns for classification
ERROR_PATTERNS = {
    'crash_loop': {
        'regex': r'CrashLoopBackOff',
        'severity': 'critical',
        'description': 'Container repeatedly crashing'
    },
    'oom_killed': {
        'regex': r'OOMKilled',
        'severity': 'critical',
        'description': 'Out of memory - container killed'
    },
    'image_pull_error': {
        'regex': r'(ImagePullBackOff|ErrImagePull)',
        'severity': 'critical',
        'description': 'Failed to pull container image'
    },
    'failed_scheduling': {
        'regex': r'FailedScheduling',
        'severity': 'critical',
        'description': 'Pod could not be scheduled'
    },
    'failed_mount': {
        'regex': r'FailedMount',
        'severity': 'critical',
        'description': 'Volume mount failed'
    },
    'evicted': {
        'regex': r'Evicted',
        'severity': 'critical',
        'description': 'Pod evicted due to resource pressure'
    },
    'connection_refused': {
        'regex': r'connection refused',
        'severity': 'error',
        'description': 'Connection refused'
    },
    'connection_timeout': {
        'regex': r'(connection timeout|i/o timeout)',
        'severity': 'error',
        'description': 'Connection timeout'
    },
    'auth_failed': {
        'regex': r'(authentication failed|unauthorized)',
        'severity': 'error',
        'description': 'Authentication failed'
    },
    'cert_error': {
        'regex': r'(certificate|x509|TLS handshake)',
        'severity': 'error',
        'description': 'Certificate or TLS error'
    },
    'readiness_probe': {
        'regex': r'Readiness probe failed',
        'severity': 'warning',
        'description': 'Readiness probe failed'
    },
    'liveness_probe': {
        'regex': r'Liveness probe failed',
        'severity': 'warning',
        'description': 'Liveness probe failed'
    },
    'admission_denied': {
        'regex': r'admission webhook.*denied',
        'severity': 'error',
        'description': 'Admission webhook denied request'
    },
    'reconciler_error': {
        'regex': r'Reconciler error',
        'severity': 'error',
        'description': 'Controller reconciliation error'
    },
}


class TestSummaryGenerator:
    """Generates comprehensive test summary from E2E artifacts."""

    def __init__(self, logs_dir: Path):
        self.logs_dir = logs_dir
        self.data = {
            'meta': {},
            'test_run': {},
            'topology': {},
            'resources': {
                'pods': [],
                'statefulsets': [],
                'deployments': [],
                'services': [],
                'secrets': [],
                'configmaps': [],
                'pvcs': [],
                'custom_resources': {},
            },
            'resource_graph': {'edges': []},
            'errors': [],
            'error_patterns': [],
            'timeline': [],
            'artifacts': {'files': [], 'summary': {}},
        }
        self.error_id_counter = 0
        self.errors_by_pattern = defaultdict(list)

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
        self.data['meta'] = {
            'generated_at': datetime.now(timezone.utc).isoformat(),
            'task_id': '[TASK_ID]',  # Filled by integration script
            'execution': 0,
            'test_type': '[TEST_TYPE]',  # Filled by integration script
        }

    def _parse_test_results(self):
        """Parse JUnit XML test results."""
        xml_report = self.logs_dir / "myreport.xml"
        if not xml_report.exists():
            self.data['test_run'] = {
                'status': 'unknown',
                'tests': {'total': 0, 'passed': 0, 'failed': 0, 'skipped': 0, 'details': []}
            }
            return

        try:
            tree = ET.parse(xml_report)
            root = tree.getroot()
            testsuite = root.find('.//testsuite')

            if testsuite is not None:
                total = int(testsuite.get('tests', 0))
                failures = int(testsuite.get('failures', 0))
                errors = int(testsuite.get('errors', 0))
                skipped = int(testsuite.get('skipped', 0))
                passed = total - failures - errors - skipped
                duration = float(testsuite.get('time', 0))

                status = 'passed' if (failures + errors) == 0 else 'failed'

                test_details = []
                for testcase in root.findall('.//testcase'):
                    test_name = testcase.get('name', '')
                    test_duration = float(testcase.get('time', 0))
                    test_file = testcase.get('file', '')
                    test_line = testcase.get('line', '')

                    failure = testcase.find('failure')
                    error = testcase.find('error')

                    if failure is not None or error is not None:
                        elem = failure if failure is not None else error
                        error_msg = elem.get('message', '')
                        test_status = 'failed'
                    else:
                        error_msg = None
                        test_status = 'passed'

                    test_details.append({
                        'name': test_name,
                        'status': test_status,
                        'duration': test_duration,
                        'error_message': error_msg,
                        'file': test_file,
                        'line': test_line,
                    })

                self.data['test_run'] = {
                    'status': status,
                    'duration_seconds': duration,
                    'tests': {
                        'total': total,
                        'passed': passed,
                        'failed': failures + errors,
                        'skipped': skipped,
                        'details': test_details,
                    }
                }
        except Exception as e:
            print(f"Warning: Failed to parse test results: {e}", file=sys.stderr)
            self.data['test_run'] = {
                'status': 'unknown',
                'tests': {'total': 0, 'passed': 0, 'failed': 0, 'skipped': 0, 'details': []}
            }

    def _detect_topology(self):
        """Detect test topology (single-cluster vs multi-cluster)."""
        # Look for diagnostics files with cluster prefixes
        diag_files = sorted(self.logs_dir.glob("*0_diagnostics.txt"))

        if len(diag_files) <= 1:
            # Single cluster
            self.data['topology'] = {
                'type': 'single-cluster',
                'clusters': [{'context': 'default', 'namespaces': [], 'role': 'single'}]
            }
        else:
            # Multi-cluster: extract contexts from filenames
            clusters = []
            for diag_file in diag_files:
                parts = diag_file.name.split('_')
                if len(parts) > 1:
                    context = parts[0]
                    # Try to determine role from context name
                    role = 'central' if 'central' in context else 'member'
                    clusters.append({'context': context, 'namespaces': [], 'role': role})

            self.data['topology'] = {
                'type': 'multi-cluster',
                'clusters': clusters
            }

    def _build_resource_inventory(self):
        """Build inventory of Kubernetes resources from YAML dumps."""
        if yaml is None:
            print("Warning: Skipping resource inventory (PyYAML not available)", file=sys.stderr)
            return

        # Parse pod dumps
        self._parse_pods()
        self._parse_statefulsets()
        self._parse_deployments()
        # Additional resource types can be added here

    def _parse_pods(self):
        """Parse pod information from z_pods.txt files."""
        if yaml is None:
            return

        pod_files = list(self.logs_dir.glob("*z_pods.txt"))
        for pod_file in pod_files:
            cluster, namespace = self._extract_cluster_namespace(pod_file.name)

            try:
                with open(pod_file, 'r') as f:
                    content = f.read()

                    # Skip header lines (e.g., "----\nPods\n----")
                    # Find the first line that starts with "apiVersion:" or "items:"
                    lines = content.split('\n')
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(('apiVersion:', 'items:')):
                            yaml_start = i
                            break

                    yaml_content = '\n'.join(lines[yaml_start:])

                    try:
                        data = yaml.safe_load(yaml_content)
                        if not data:
                            continue

                        # Handle two formats:
                        # 1. List format: {apiVersion: v1, items: [...]}
                        # 2. Individual documents separated by ---
                        pods = []
                        if isinstance(data, dict) and 'items' in data:
                            # List format
                            pods = data.get('items', [])
                        elif isinstance(data, dict) and data.get('kind') == 'Pod':
                            # Single pod document
                            pods = [data]

                        for pod in pods:
                            if not pod or pod.get('kind') != 'Pod':
                                continue

                            pod_info = self._extract_pod_info(pod, cluster, namespace, pod_file.name)
                            if pod_info:
                                self.data['resources']['pods'].append(pod_info)
                    except yaml.YAMLError as e:
                        print(f"Warning: YAML parse error in {pod_file.name}: {e}", file=sys.stderr)
                        continue
            except Exception as e:
                print(f"Warning: Failed to parse {pod_file.name}: {e}", file=sys.stderr)

    def _extract_pod_info(self, pod: Dict, cluster: str, namespace: str, source_file: str) -> Optional[Dict]:
        """Extract relevant pod information."""
        try:
            metadata = pod.get('metadata', {})
            spec = pod.get('spec', {})
            status = pod.get('status', {})

            pod_name = metadata.get('name', 'unknown')
            pod_id = f"{cluster}/{namespace}/{pod_name}"

            # Container information
            containers = []
            container_statuses = status.get('containerStatuses', [])
            for cs in container_statuses:
                container_name = cs.get('name', 'unknown')
                state = cs.get('state', {})
                last_state = cs.get('lastState', {})

                # Determine current state
                current_state = 'unknown'
                if 'running' in state:
                    current_state = 'running'
                elif 'waiting' in state:
                    current_state = 'waiting'
                elif 'terminated' in state:
                    current_state = 'terminated'

                exit_code = None
                if 'terminated' in last_state:
                    exit_code = last_state['terminated'].get('exitCode')

                containers.append({
                    'name': container_name,
                    'state': current_state,
                    'ready': cs.get('ready', False),
                    'restarts': cs.get('restartCount', 0),
                    'last_state': 'terminated' if last_state.get('terminated') else None,
                    'exit_code': exit_code,
                })

            # Owner reference
            owner_refs = metadata.get('ownerReferences', [])
            owner_ref = None
            if owner_refs:
                owner = owner_refs[0]
                owner_ref = f"{owner.get('kind')}/{owner.get('name')}"

            # Conditions
            conditions = []
            for cond in status.get('conditions', []):
                conditions.append({
                    'type': cond.get('type'),
                    'status': cond.get('status'),
                    'reason': cond.get('reason'),
                })

            # Find related files
            related_files = self._find_pod_files(cluster, namespace, pod_name)

            return {
                'id': pod_id,
                'name': pod_name,
                'namespace': namespace,
                'cluster': cluster,
                'phase': status.get('phase', 'unknown'),
                'ready': self._calculate_ready_status(containers),
                'restarts': sum(c['restarts'] for c in containers),
                'owner_ref': owner_ref,
                'containers': containers,
                'conditions': conditions,
                'files': related_files,
            }
        except Exception as e:
            print(f"Warning: Failed to extract pod info: {e}", file=sys.stderr)
            return None

    def _calculate_ready_status(self, containers: List[Dict]) -> str:
        """Calculate ready status string (e.g., '2/3')."""
        if not containers:
            return '0/0'
        ready_count = sum(1 for c in containers if c.get('ready', False))
        return f"{ready_count}/{len(containers)}"

    def _build_file_prefix(self, cluster: str, namespace: str) -> str:
        """Build the file prefix matching dump_namespace convention."""
        if cluster != 'default':
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
            'describe': None,
            'logs': [],
            'agent_logs': [],
            'config': [],
            'health': None,
        }

        for f in self.logs_dir.iterdir():
            if not f.is_file() or not f.name.startswith(pod_prefix):
                continue

            suffix = f.name[len(pod_prefix):]

            if suffix == 'pod-describe.txt':
                files['describe'] = f.name
            elif suffix == 'agent-health-status.json':
                files['health'] = f.name
            elif suffix in ('cluster-config.json', 'automation-mongod.conf'):
                files['config'].append(f.name)
            elif suffix.endswith('.log') and any(suffix.startswith(a) for a in (
                'agent-verbose', 'agent.', 'agent-stderr',
                'monitoring-agent',
            )):
                files['agent_logs'].append(f.name)
            elif suffix.endswith('.log'):
                files['logs'].append(f.name)

        return files

    def _parse_statefulsets(self):
        """Parse StatefulSet information."""
        if yaml is None:
            return

        sts_files = list(self.logs_dir.glob("*z_statefulsets.txt"))
        for sts_file in sts_files:
            cluster, namespace = self._extract_cluster_namespace(sts_file.name)

            try:
                with open(sts_file, 'r') as f:
                    content = f.read()

                    # Skip header lines
                    lines = content.split('\n')
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(('apiVersion:', 'items:')):
                            yaml_start = i
                            break

                    yaml_content = '\n'.join(lines[yaml_start:])

                    try:
                        data = yaml.safe_load(yaml_content)
                        if not data:
                            continue

                        # Handle list format or individual documents
                        statefulsets = []
                        if isinstance(data, dict) and 'items' in data:
                            statefulsets = data.get('items', [])
                        elif isinstance(data, dict) and data.get('kind') == 'StatefulSet':
                            statefulsets = [data]

                        for sts in statefulsets:
                            if not sts or sts.get('kind') != 'StatefulSet':
                                continue

                            sts_info = self._extract_statefulset_info(sts, cluster, namespace, sts_file.name)
                            if sts_info:
                                self.data['resources']['statefulsets'].append(sts_info)
                    except yaml.YAMLError as e:
                        print(f"Warning: YAML parse error in {sts_file.name}: {e}", file=sys.stderr)
                        continue
            except Exception as e:
                print(f"Warning: Failed to parse {sts_file.name}: {e}", file=sys.stderr)

    def _extract_statefulset_info(self, sts: Dict, cluster: str, namespace: str, source_file: str) -> Optional[Dict]:
        """Extract relevant StatefulSet information."""
        try:
            metadata = sts.get('metadata', {})
            spec = sts.get('spec', {})
            status = sts.get('status', {})

            sts_name = metadata.get('name', 'unknown')
            sts_id = f"{cluster}/{namespace}/{sts_name}"

            owner_refs = metadata.get('ownerReferences', [])
            owner_ref = None
            if owner_refs:
                owner = owner_refs[0]
                owner_ref = f"{owner.get('kind')}/{owner.get('name')}"

            return {
                'id': sts_id,
                'name': sts_name,
                'namespace': namespace,
                'cluster': cluster,
                'desired_replicas': spec.get('replicas', 0),
                'current_replicas': status.get('currentReplicas', 0),
                'ready_replicas': status.get('readyReplicas', 0),
                'owner_ref': owner_ref,
                'selector': spec.get('selector', {}),
                'files': {'yaml': source_file},
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
                with open(deploy_file, 'r') as f:
                    content = f.read()

                    # Skip header lines
                    lines = content.split('\n')
                    yaml_start = 0
                    for i, line in enumerate(lines):
                        if line.strip().startswith(('apiVersion:', 'items:')):
                            yaml_start = i
                            break

                    yaml_content = '\n'.join(lines[yaml_start:])

                    try:
                        data = yaml.safe_load(yaml_content)
                        if not data:
                            continue

                        # Handle list format or individual documents
                        deployments = []
                        if isinstance(data, dict) and 'items' in data:
                            deployments = data.get('items', [])
                        elif isinstance(data, dict) and data.get('kind') == 'Deployment':
                            deployments = [data]

                        for deploy in deployments:
                            if not deploy or deploy.get('kind') != 'Deployment':
                                continue

                            deploy_info = self._extract_deployment_info(deploy, cluster, namespace, deploy_file.name)
                            if deploy_info:
                                self.data['resources']['deployments'].append(deploy_info)
                    except yaml.YAMLError as e:
                        print(f"Warning: YAML parse error in {deploy_file.name}: {e}", file=sys.stderr)
                        continue
            except Exception as e:
                print(f"Warning: Failed to parse {deploy_file.name}: {e}", file=sys.stderr)

    def _extract_deployment_info(self, deploy: Dict, cluster: str, namespace: str, source_file: str) -> Optional[Dict]:
        """Extract relevant Deployment information."""
        try:
            metadata = deploy.get('metadata', {})
            spec = deploy.get('spec', {})
            status = deploy.get('status', {})

            deploy_name = metadata.get('name', 'unknown')
            deploy_id = f"{cluster}/{namespace}/{deploy_name}"

            return {
                'id': deploy_id,
                'name': deploy_name,
                'namespace': namespace,
                'cluster': cluster,
                'desired_replicas': spec.get('replicas', 0),
                'current_replicas': status.get('replicas', 0),
                'ready_replicas': status.get('readyReplicas', 0),
                'files': {'yaml': source_file},
            }
        except Exception as e:
            print(f"Warning: Failed to extract Deployment info: {e}", file=sys.stderr)
            return None

    def _extract_cluster_namespace(self, filename: str) -> Tuple[str, str]:
        """Extract cluster and namespace from filename.

        File naming comes from dump_diagnostic_information.sh:
          dump_namespace builds prefix as "${context_prefix}_${namespace}_"

        Multi-cluster: context_prefix = "${context}_" → files are "{context}__{namespace}_{rest}"
          e.g. kind-e2e-cluster-1__a-1770815153-mxfvgwckn0z_z_pods.txt

        Single-cluster: context_prefix = "" → files are "_{namespace}_{rest}" or "{namespace}_{rest}"
          e.g. _a-1770815153-mxfvgwckn0z_z_pods.txt

        The '__' double underscore is the key separator between context and namespace.
        K8s namespaces never contain underscores, so the first '_' after the namespace
        separates it from the rest of the filename.
        """
        # Multi-cluster: split on '__'
        if '__' in filename:
            context, rest = filename.split('__', 1)
            # Namespace ends at first '_' (K8s namespaces have no underscores)
            if '_' in rest:
                namespace, _ = rest.split('_', 1)
            else:
                namespace = rest
            return context, namespace

        # Single-cluster: may start with '_' from prefix="" + "_${namespace}_"
        name = filename.lstrip('_')
        if '_' in name:
            namespace, _ = name.split('_', 1)
            return 'default', namespace

        return 'default', 'default'

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
            with open(log_file, 'r', errors='ignore') as f:
                error_count = 0
                for line_no, line in enumerate(f, 1):
                    if error_count >= max_errors_per_file:
                        break

                    # Check each pattern
                    for pattern_name, pattern_info in ERROR_PATTERNS.items():
                        regex = re.compile(pattern_info['regex'], re.IGNORECASE)
                        if regex.search(line):
                            error_id = f"err-{self.error_id_counter:04d}"
                            self.error_id_counter += 1

                            # Try to extract timestamp
                            timestamp = self._extract_timestamp(line)

                            error_entry = {
                                'id': error_id,
                                'timestamp': timestamp,
                                'severity': pattern_info['severity'],
                                'pattern': pattern_name,
                                'source': {
                                    'resource': resource,
                                    'container': container,
                                    'file': log_file.name,
                                    'line': line_no,
                                },
                                'message': line.strip()[:200],  # Truncate long lines
                                'context': line.strip()[:500],
                            }

                            self.data['errors'].append(error_entry)
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
            rest = filename[len(prefix):]
        rest = re.sub(r'\.(log|txt|json|conf)$', '', rest)

        # Known suffixes from dump_pod_logs in dump_diagnostic_information.sh
        container = 'unknown'
        pod_name = rest

        known_suffixes = [
            '-agent-verbose', '-agent-stderr', '-agent',
            '-monitoring-agent-verbose', '-monitoring-agent-stdout', '-monitoring-agent',
            '-mongodb-agent-container', '-mongod-container',
            '-mongodb-enterprise-database',
            '-mongodb', '-launcher', '-readiness',
            '-istio-proxy', '-keepalive',
        ]

        for suffix in sorted(known_suffixes, key=len, reverse=True):
            if rest.endswith(suffix):
                pod_name = rest[:-len(suffix)]
                container = suffix.lstrip('-')
                break

        resource = f"Pod/{cluster}/{namespace}/{pod_name}"
        return resource, container

    def _extract_timestamp(self, line: str) -> Optional[str]:
        """Try to extract ISO timestamp from log line."""
        # Common patterns:
        # 2025-02-11T07:23:45Z
        # 2025-02-11 07:23:45
        patterns = [
            r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?)',
            r'(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})',
        ]

        for pattern in patterns:
            match = re.search(pattern, line)
            if match:
                timestamp_str = match.group(1)
                # Normalize to ISO format
                if 'T' not in timestamp_str:
                    timestamp_str = timestamp_str.replace(' ', 'T') + 'Z'
                elif not timestamp_str.endswith('Z'):
                    timestamp_str += 'Z'
                return timestamp_str

        return None

    def _build_timeline(self):
        """Build timeline from events and log errors."""
        # Parse Kubernetes events
        self._parse_k8s_events()

        # Add log errors to timeline (already extracted)
        for error in self.data['errors']:
            if error.get('timestamp'):
                self.data['timeline'].append({
                    'timestamp': error['timestamp'],
                    'type': 'log_error',
                    'severity': error['severity'],
                    'resource': error['source']['resource'],
                    'message': error['message'],
                    'error_id': error['id'],
                })

        # Sort timeline by timestamp (handle None values)
        self.data['timeline'].sort(key=lambda x: x.get('timestamp') or '')

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
                with open(event_file, 'r') as f:
                    data = yaml.safe_load(f)

                if not data or 'items' not in data:
                    continue

                for event in data['items']:
                    if not event or event.get('kind') != 'Event':
                        continue

                    # Use lastTimestamp (most recent occurrence) or firstTimestamp
                    timestamp = event.get('lastTimestamp') or event.get('firstTimestamp')
                    event_type = event.get('type', 'Normal')  # Normal or Warning
                    reason = event.get('reason', '')
                    message = event.get('message', '')
                    count = event.get('count', 1)

                    # Extract the involved object (e.g., Pod/my-pod-0)
                    involved = event.get('involvedObject', {})
                    obj_kind = involved.get('kind', '')
                    obj_name = involved.get('name', '')
                    resource = f"{obj_kind}/{obj_name}" if obj_kind else 'unknown'

                    severity = 'warning' if event_type == 'Warning' else ''

                    self.data['timeline'].append({
                        'timestamp': timestamp,
                        'type': 'k8s_event',
                        'event_type': event_type,
                        'reason': reason,
                        'resource': resource,
                        'message': message,
                        'count': count,
                        'cluster': cluster,
                        'namespace': namespace,
                        'severity': severity,
                    })

            except Exception as e:
                print(f"Warning: Failed to parse events from {event_file.name}: {e}", file=sys.stderr)

    def _build_resource_graph(self):
        """Build resource relationship graph."""
        # Build ownership edges
        for pod in self.data['resources']['pods']:
            if pod.get('owner_ref'):
                self.data['resource_graph']['edges'].append({
                    'from': f"Pod/{pod['id']}",
                    'to': f"{pod['owner_ref']}/{pod['cluster']}/{pod['namespace']}",
                    'relationship': 'owned_by',
                })

        for sts in self.data['resources']['statefulsets']:
            if sts.get('owner_ref'):
                self.data['resource_graph']['edges'].append({
                    'from': f"StatefulSet/{sts['id']}",
                    'to': f"{sts['owner_ref']}/{sts['cluster']}/{sts['namespace']}",
                    'relationship': 'owned_by',
                })

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
                'name': file_path.name,
                'type': file_type,
                'size_bytes': size,
                'cluster': cluster,
                'namespace': namespace,
            }

            self.data['artifacts']['files'].append(file_entry)
            by_cluster[cluster][namespace].append(file_entry)

        file_count = sum(1 for f in all_files if f.is_file())
        self.data['artifacts']['summary'] = {
            'total_files': file_count,
            'total_size_bytes': total_size,
            'by_type': dict(by_type),
        }
        self.data['artifacts']['by_cluster'] = {
            cluster: {ns: files for ns, files in namespaces.items()}
            for cluster, namespaces in by_cluster.items()
        }

    def _classify_file_type(self, filename: str) -> str:
        """Classify file type from filename.

        The 'z_' prefix appears after the namespace prefix, not at the start.
        e.g., kind-e2e-cluster-1__ns-12345_z_pods.txt
        """
        if filename.endswith('.log'):
            return 'log'
        elif 'pod-describe' in filename:
            return 'describe'
        elif '_z_' in filename and filename.endswith('.txt'):
            return 'resource_dump'
        elif '_0_diagnostics.txt' in filename:
            return 'diagnostics'
        elif 'events_detailed.yaml' in filename:
            return 'events'
        elif 'events.txt' in filename:
            return 'events'
        elif 'agent-health-status.json' in filename:
            return 'health'
        elif 'automation-mongod.conf' in filename:
            return 'config'
        elif 'cluster-config.json' in filename:
            return 'config'
        elif filename.endswith('.xml'):
            return 'test_results'
        elif filename.endswith('.json'):
            return 'json'
        elif filename.endswith('.yaml'):
            return 'yaml'
        else:
            return 'other'

    def _embed_log_tails(self, tail_lines: int = 500, max_total_bytes: int = 20 * 1024 * 1024):
        """Embed file content for inline viewing in the modal.

        For log files: embeds the last N lines (most recent activity).
        For structured files (YAML, JSON, conf, txt dumps): embeds from the beginning
        since these are not append-only streams.
        """
        self.data['log_tails'] = {}
        total_bytes = 0

        viewable_extensions = {'.log', '.txt', '.json', '.conf', '.yaml', '.xml'}
        skip_patterns = ['test-summary']

        # Structured files should be read from the beginning, not tailed
        head_extensions = {'.yaml', '.json', '.conf', '.xml'}
        head_suffixes = {'_describe.txt', '_diagnostics.txt'}

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
                with open(file_path, 'r', errors='replace') as f:
                    lines = f.readlines()

                # Structured files: read from beginning (head)
                # Log files: read from end (tail)
                use_head = (file_path.suffix in head_extensions
                            or '_z_' in file_path.name
                            or any(file_path.name.endswith(s) for s in head_suffixes))

                if use_head:
                    shown = lines[:tail_lines]
                    truncated = len(lines) > tail_lines
                else:
                    shown = lines[-tail_lines:]
                    truncated = len(lines) > tail_lines

                content = ''.join(shown)

                # Cap total embedded size
                content_bytes = len(content.encode('utf-8', errors='replace'))
                if total_bytes + content_bytes > max_total_bytes:
                    print(f"  Skipping {file_path.name} (total size cap reached)", file=sys.stderr)
                    continue

                total_bytes += content_bytes
                self.data['log_tails'][file_path.name] = {
                    'content': content,
                    'total_lines': len(lines),
                    'shown_lines': len(shown),
                    'truncated': truncated,
                    'mode': 'head' if use_head else 'tail',
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
                error = next((e for e in self.data['errors'] if e['id'] == error_id), None)
                if error:
                    affected_resources.add(error['source']['resource'])
                    if error.get('timestamp'):
                        if first_timestamp is None or error['timestamp'] < first_timestamp:
                            first_timestamp = error['timestamp']
                        if last_timestamp is None or error['timestamp'] > last_timestamp:
                            last_timestamp = error['timestamp']

            self.data['error_patterns'].append({
                'pattern': pattern_name,
                'count': len(error_ids),
                'description': pattern_info.get('description', 'Unknown pattern'),
                'severity': pattern_info.get('severity', 'unknown'),
                'affected_resources': sorted(list(affected_resources)),
                'error_ids': error_ids[:10],  # Limit to first 10
                'first_occurrence': first_timestamp,
                'last_occurrence': last_timestamp,
            })

        # Sort by count (most common first)
        self.data['error_patterns'].sort(key=lambda x: x['count'], reverse=True)


def _render_timeline_entry(idx: int, entry: Dict) -> str:
    """Render a single timeline entry."""
    severity = entry.get('severity', '')
    event_type = entry.get('event_type', '')
    reason = entry.get('reason', '')
    resource = entry.get('resource', '')
    message = entry.get('message', '')
    timestamp = entry.get('timestamp')
    count = entry.get('count', 1)

    # Format time as HH:MM:SS for display, full timestamp on hover
    time_display = ''
    if timestamp:
        time_display = f"<span class='timeline-timestamp' title='{escape(str(timestamp))}'>{escape(str(timestamp)[11:19])}</span>"

    # Severity badge
    badge_class = ''
    if severity == 'warning' or event_type == 'Warning':
        badge_class = 'badge-warning'
    elif severity in ('error', 'critical'):
        badge_class = 'badge-error'

    # Reason tag
    reason_html = ''
    if reason:
        reason_html = f"<span class='event-reason'>{escape(reason)}</span>"

    # Count indicator
    count_html = ''
    if count and count > 1:
        count_html = f"<span class='event-count'>x{count}</span>"

    # Message with expand for long content
    if len(message) > 200:
        msg_html = f"<div class='timeline-message'>{escape(message[:200])}...<span class='expand-link' onclick='expandTimelineMsg(this)'>expand</span></div>"
        msg_html += f"<div class='timeline-message-full' style='display:none;'>{escape(message)}</div>"
    else:
        msg_html = f"<div class='timeline-message'>{escape(message)}</div>"

    return f"""<div class='timeline-entry {severity} {badge_class}'>
        <div class='timeline-number'>{idx + 1}</div>
        <div class='timeline-content'>
            <div class='timeline-header'>
                {time_display}
                {reason_html}
                {count_html}
                <span class='timeline-resource'>{escape(str(resource))}</span>
            </div>
            {msg_html}
        </div>
    </div>"""


def _render_cluster_resources(data: Dict, log_tails: Dict) -> str:
    """Render cluster resource dump files as clickable links grouped by category.

    Shows all z_ dump files, events, diagnostics, and other structural files
    organized by cluster/namespace so the user can quickly access any resource.
    """
    by_cluster = data['artifacts'].get('by_cluster', {})
    if not by_cluster:
        return ''

    # Resource file categories - label and matching patterns
    resource_categories = [
        ('ConfigMaps', 'z_configmaps'),
        ('Services', 'z_services'),
        ('PVCs', 'z_persistent_volume_claims'),
        ('Secrets', 'z_secret_'),
        ('Deployments YAML', 'z_deployments.txt'),
        ('Deployments Describe', 'z_deployments_describe'),
        ('StatefulSets', 'z_statefulsets'),
        ('Roles', 'z_roles.txt'),
        ('RoleBindings', 'z_rolebindings'),
        ('ClusterRoles', 'z_clusterroles.txt'),
        ('ClusterRoleBindings', 'z_clusterrolebindings'),
        ('ServiceAccounts', 'z_service_accounts'),
        ('Webhooks', 'z_validatingwebhook'),
        ('CRDs', 'z_mongodb_crds'),
        ('Nodes', 'z_nodes_detailed'),
        ('Events (YAML)', 'events_detailed.yaml'),
        ('Events (text)', 'events.txt'),
        ('Diagnostics', '0_diagnostics'),
        ('Metrics', 'metrics_'),
    ]

    html = "<h3 style='margin-top: 20px;'>Cluster Resources</h3>"

    for cluster in sorted(by_cluster.keys()):
        namespaces = by_cluster[cluster]
        for ns in sorted(namespaces.keys()):
            files = namespaces[ns]
            label = f"{cluster}/{ns}" if cluster != 'default' else ns

            # Collect resource files by category
            categorized = []
            uncategorized = []
            seen = set()

            for cat_label, pattern in resource_categories:
                matching = [f for f in files
                            if pattern in f['name']
                            and f['name'] not in seen]
                if matching:
                    categorized.append((cat_label, matching))
                    seen.update(f['name'] for f in matching)

            # Remaining z_ or structural files not categorized
            for f in files:
                if f['name'] not in seen and (
                    '_z_' in f['name']
                    or f['type'] in ('events', 'diagnostics', 'config', 'health')
                ):
                    uncategorized.append(f)
                    seen.add(f['name'])

            if not categorized and not uncategorized:
                continue

            html += f"<div class='cluster-resources-group'>"
            html += f"<h4 class='resource-name' style='margin: 12px 0 6px 0;'>{escape(label)}</h4>"
            html += "<div class='cluster-resources-grid'>"

            for cat_label, cat_files in categorized:
                for f in cat_files:
                    fname = f['name']
                    # Short display name: strip the namespace prefix
                    short = fname.split('_z_')[-1] if '_z_' in fname else fname.split('_')[-1] if '_' in fname else fname
                    # For secrets, show the secret name
                    if 'z_secret_' in fname:
                        short = fname.split('z_secret_')[-1]
                    display_label = f"{cat_label}: {short}" if 'z_secret_' in fname else cat_label if len(cat_files) == 1 else f"{cat_label}: {short}"

                    if fname in log_tails:
                        html += f"<code class='file-tag-link' onclick=\"openLogModal('{escape(fname)}')\" title='{escape(fname)}'>{escape(display_label)}</code>"
                    else:
                        html += f"<code class='file-tag' title='{escape(fname)}'>{escape(display_label)}</code>"

            for f in uncategorized:
                fname = f['name']
                short = fname.split('_')[-1] if '_' in fname else fname
                if fname in log_tails:
                    html += f"<code class='file-tag-link' onclick=\"openLogModal('{escape(fname)}')\" title='{escape(fname)}'>{escape(short)}</code>"
                else:
                    html += f"<code class='file-tag' title='{escape(fname)}'>{escape(short)}</code>"

            html += "</div></div>"

    return html


def _render_artifacts_by_cluster(data: Dict) -> str:
    """Render artifacts as a compact cluster summary table.

    The per-pod file details are already in the Resource Inventory section.
    This section is a high-level index: what data do we have, per cluster.
    """
    by_cluster = data['artifacts'].get('by_cluster', {})
    if not by_cluster:
        return '<p>No artifacts found</p>'

    # Build per-cluster type counts, skipping 'default' noise
    cluster_stats = []
    type_columns = set()
    for cluster in sorted(by_cluster.keys()):
        namespaces = by_cluster[cluster]
        for ns in sorted(namespaces.keys()):
            files = namespaces[ns]
            label = f"{cluster}/{ns}" if cluster != 'default' else ns

            by_type = defaultdict(lambda: {'count': 0, 'size': 0})
            for f in files:
                by_type[f['type']]['count'] += 1
                by_type[f['type']]['size'] += f['size_bytes']

            type_columns.update(by_type.keys())
            total_size = sum(f['size_bytes'] for f in files)
            cluster_stats.append({
                'label': label,
                'total': len(files),
                'total_size': total_size,
                'by_type': dict(by_type),
            })

    if not cluster_stats:
        return '<p>No cluster artifacts found</p>'

    # Render as a compact table
    col_order = ['log', 'agent_log', 'describe', 'resource_dump', 'events',
                 'health', 'config', 'diagnostics', 'json', 'other']
    cols = [c for c in col_order if c in type_columns]
    cols += sorted(type_columns - set(col_order))

    col_labels = {
        'log': 'Logs', 'describe': 'Describe', 'resource_dump': 'Resources',
        'events': 'Events', 'health': 'Health', 'config': 'Config',
        'diagnostics': 'Diag', 'json': 'JSON', 'yaml': 'YAML',
        'other': 'Other', 'test_results': 'Tests',
    }

    html = "<table class='artifacts-table'><tr><th>Cluster</th><th>Files</th><th>Size</th>"
    for col in cols:
        html += f"<th>{escape(col_labels.get(col, col))}</th>"
    html += "</tr>"

    for stat in cluster_stats:
        html += f"<tr><td class='resource-name'>{escape(stat['label'])}</td>"
        html += f"<td>{stat['total']}</td>"
        html += f"<td>{_format_size(stat['total_size'])}</td>"
        for col in cols:
            info = stat['by_type'].get(col)
            if info:
                html += f"<td>{info['count']}</td>"
            else:
                html += "<td class='empty-cell'>-</td>"
        html += "</tr>"

    html += "</table>"

    return html


def _format_size(size_bytes: int) -> str:
    """Format byte size for display."""
    if size_bytes == 0:
        return '0B'
    elif size_bytes < 1024:
        return f'{size_bytes}B'
    elif size_bytes < 1024 * 1024:
        return f'{size_bytes / 1024:.1f}KB'
    else:
        return f'{size_bytes / 1024 / 1024:.1f}MB'


def generate_html(data: Dict[str, Any], test_name: str, variant: str, status: str) -> str:
    """Generate interactive HTML with embedded JSON."""
    # Extract log tails before serializing — keep the copyable JSON clean
    log_tails = data.pop('log_tails', {})
    log_tails_json = json.dumps(log_tails)

    json_data = json.dumps(data, indent=2)
    escaped_json = escape(json_data)

    # Calculate quick stats
    failed_tests = data['test_run'].get('tests', {}).get('failed', 0)
    total_tests = data['test_run'].get('tests', {}).get('total', 0)

    unhealthy_pods = sum(1 for pod in data['resources']['pods']
                         if pod.get('phase') != 'Running' or pod.get('restarts', 0) > 0)

    top_errors = data['error_patterns'][:5]

    status_color = "#28a745" if status == "PASSED" else "#dc3545" if status == "FAILED" else "#6c757d"

    # Generate failed tests HTML with expandable errors
    failed_tests_html = ""
    if failed_tests > 0:
        failed_tests_html = "<h2>Failed Tests</h2><div class='section'>"
        for idx, test in enumerate(data['test_run'].get('tests', {}).get('details', [])):
            if test['status'] == 'failed':
                failed_tests_html += "<div class='failed-test'>"
                failed_tests_html += f"<div class='failed-test-header'>"
                failed_tests_html += f"<span class='test-name'><strong>{escape(test['name'])}</strong></span>"
                failed_tests_html += f"<span class='test-duration'>{test['duration']:.1f}s</span>"
                failed_tests_html += "</div>"
                error_msg = test.get('error_message', 'Unknown error')
                if len(error_msg) > 200:
                    failed_tests_html += f"<div class='test-error'>{escape(error_msg[:200])}..."
                    failed_tests_html += f"<span class='expand-link' onclick='toggleTestError(\"test-error-{idx}\")'>expand</span></div>"
                    failed_tests_html += f"<div id='test-error-{idx}' class='test-error-full' style='display: none;'>{escape(error_msg)}</div>"
                else:
                    failed_tests_html += f"<div class='test-error'>{escape(error_msg)}</div>"
                if test.get('file'):
                    failed_tests_html += f"<div class='test-location'>{escape(test['file'])}"
                    if test.get('line'):
                        failed_tests_html += f":{escape(test['line'])}"
                    failed_tests_html += "</div>"
                failed_tests_html += "</div>"
        failed_tests_html += "</div>"

    # Generate resource inventory HTML with per-pod file details
    resource_html = "<h2>Resource Inventory</h2>"
    resource_html += f"<div class='section'><h3>Pods ({len(data['resources']['pods'])})</h3>"
    if data['resources']['pods']:
        resource_html += "<table><tr><th>#</th><th>Name</th><th>Cluster</th><th>Phase</th><th>Ready</th><th>Restarts</th><th>Logs</th><th>Agent</th></tr>"
        for idx, pod in enumerate(data['resources']['pods'][:50], 1):
            phase_class = 'healthy' if pod['phase'] == 'Running' else 'unhealthy'
            log_count = len(pod['files'].get('logs', []))
            agent_count = len(pod['files'].get('agent_logs', []))
            has_describe = '1' if pod['files'].get('describe') else '-'
            has_health = '1' if pod['files'].get('health') else '-'
            resource_html += f"<tr class='{phase_class}' onclick='togglePodFiles(\"pod-files-{idx}\")' style='cursor: pointer;'>"
            resource_html += f"<td class='row-number'>{idx}</td>"
            resource_html += f"<td class='resource-name'>{escape(pod['name'])}</td>"
            resource_html += f"<td>{escape(pod['cluster'])}</td>"
            resource_html += f"<td><span class='badge badge-{phase_class}'>{escape(pod['phase'])}</span></td>"
            resource_html += f"<td>{escape(pod['ready'])}</td>"
            resource_html += f"<td>{pod['restarts']}</td>"
            resource_html += f"<td>{log_count}</td>"
            resource_html += f"<td>{agent_count}</td>"
            resource_html += "</tr>"
            # Expandable file details row
            all_files = (pod['files'].get('logs', []) + pod['files'].get('agent_logs', [])
                         + pod['files'].get('config', []))
            if pod['files'].get('describe'):
                all_files.append(pod['files']['describe'])
            if pod['files'].get('health'):
                all_files.append(pod['files']['health'])
            resource_html += f"<tr id='pod-files-{idx}' class='pod-files-row' style='display: none;'>"
            resource_html += f"<td colspan='8'><div class='pod-files-detail'>"
            if all_files:
                for f in sorted(all_files):
                    file_label = f.split('_')[-1] if '_' in f else f
                    # Make viewable files clickable
                    if f in log_tails:
                        resource_html += f"<code class='file-tag-link' onclick=\"event.stopPropagation(); openLogModal('{escape(f)}')\">{escape(file_label)}</code> "
                    else:
                        resource_html += f"<code class='file-tag'>{escape(file_label)}</code> "
            else:
                resource_html += "<span class='truncation-note'>No files found for this pod</span>"
            resource_html += "</div></td></tr>"
        resource_html += "</table>"
        if len(data['resources']['pods']) > 50:
            resource_html += f"<p class='truncation-note'>Showing first 50 of {len(data['resources']['pods'])} pods.</p>"
    else:
        resource_html += "<p>No pods found</p>"

    # StatefulSets
    resource_html += f"<h3 style='margin-top: 20px;'>StatefulSets ({len(data['resources']['statefulsets'])})</h3>"
    if data['resources']['statefulsets']:
        resource_html += "<table><tr><th>#</th><th>Name</th><th>Cluster</th><th>Replicas</th><th>Owner</th><th>YAML</th></tr>"
        for idx, sts in enumerate(data['resources']['statefulsets'][:50], 1):
            readiness_class = 'healthy' if sts['ready_replicas'] == sts['desired_replicas'] else 'unhealthy'
            yaml_file = sts.get('files', {}).get('yaml', '')
            resource_html += f"<tr class='{readiness_class}'>"
            resource_html += f"<td class='row-number'>{idx}</td>"
            resource_html += f"<td class='resource-name'>{escape(sts['name'])}</td>"
            resource_html += f"<td>{escape(sts['cluster'])}</td>"
            resource_html += f"<td>{sts['ready_replicas']}/{sts['desired_replicas']}</td>"
            resource_html += f"<td class='resource-name'>{escape(sts.get('owner_ref', '-') or '-')}</td>"
            if yaml_file and yaml_file in log_tails:
                resource_html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(yaml_file)}')\">view</code></td>"
            else:
                resource_html += "<td>-</td>"
            resource_html += "</tr>"
        resource_html += "</table>"
    else:
        resource_html += "<p>No StatefulSets found</p>"

    # Deployments
    resource_html += f"<h3 style='margin-top: 20px;'>Deployments ({len(data['resources']['deployments'])})</h3>"
    if data['resources']['deployments']:
        resource_html += "<table><tr><th>#</th><th>Name</th><th>Cluster</th><th>Replicas</th><th>YAML</th></tr>"
        for idx, deploy in enumerate(data['resources']['deployments'][:50], 1):
            readiness_class = 'healthy' if deploy['ready_replicas'] == deploy['desired_replicas'] else 'unhealthy'
            yaml_file = deploy.get('files', {}).get('yaml', '')
            resource_html += f"<tr class='{readiness_class}'>"
            resource_html += f"<td class='row-number'>{idx}</td>"
            resource_html += f"<td class='resource-name'>{escape(deploy['name'])}</td>"
            resource_html += f"<td>{escape(deploy['cluster'])}</td>"
            resource_html += f"<td>{deploy['ready_replicas']}/{deploy['desired_replicas']}</td>"
            if yaml_file and yaml_file in log_tails:
                resource_html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(yaml_file)}')\">view</code></td>"
            else:
                resource_html += "<td>-</td>"
            resource_html += "</tr>"
        resource_html += "</table>"
    else:
        resource_html += "<p>No Deployments found</p>"

    # Cluster Resources - clickable links to all z_ dump files and other useful files
    resource_html += _render_cluster_resources(data, log_tails)

    resource_html += "</div>"

    # Generate error catalog HTML with expandable samples
    error_catalog_html = "<h2>Error Catalog</h2><div class='section'>"
    if top_errors:
        for idx, pattern in enumerate(top_errors):
            severity_class = pattern['severity']
            error_catalog_html += f"<div class='error-pattern {severity_class}'>"
            error_catalog_html += f"<h4>{escape(pattern['description'])} ({pattern['count']} occurrences)</h4>"
            error_catalog_html += f"<p><strong>Pattern:</strong> <code>{escape(pattern['pattern'])}</code></p>"
            error_catalog_html += f"<p><strong>Affected resources:</strong> {len(pattern['affected_resources'])}</p>"

            # Show sample errors
            sample_error_ids = pattern.get('error_ids', [])[:3]
            if sample_error_ids:
                error_catalog_html += f"<button class='toggle-btn' onclick='toggleSamples(\"samples-{idx}\")'>Show sample errors ▼</button>"
                error_catalog_html += f"<div id='samples-{idx}' class='sample-errors' style='display: none;'>"
                for error_id in sample_error_ids:
                    error = next((e for e in data['errors'] if e['id'] == error_id), None)
                    if error:
                        error_catalog_html += "<div class='sample-error'>"
                        error_catalog_html += f"<div class='sample-error-header'>"
                        error_catalog_html += f"<span class='error-source'>{escape(error['source'].get('file', 'unknown'))}:{error['source'].get('line', '?')}</span>"
                        if error.get('timestamp'):
                            error_catalog_html += f"<span class='error-time'>{escape(error['timestamp'])}</span>"
                        error_catalog_html += "</div>"
                        message = error.get('message', '')
                        if len(message) > 150:
                            error_catalog_html += f"<div class='error-message'>{escape(message[:150])}..."
                            error_catalog_html += f"<span class='expand-link' onclick='toggleExpand(this)'>expand</span></div>"
                            error_catalog_html += f"<div class='error-message-full' style='display: none;'>{escape(message)}</div>"
                        else:
                            error_catalog_html += f"<div class='error-message'>{escape(message)}</div>"
                        error_catalog_html += "</div>"
                error_catalog_html += "</div>"

            error_catalog_html += "</div>"
    else:
        error_catalog_html += "<p>No errors detected</p>"
    error_catalog_html += "</div>"

    html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>E2E Test Summary - {escape(test_name)}</title>
    <style>
        * {{ box-sizing: border-box; }}
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            margin: 0;
            padding: 20px;
            background: #f5f5f5;
        }}
        .container {{ max-width: 1400px; margin: 0 auto; background: white; padding: 30px; border-radius: 8px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }}
        h1 {{ margin-top: 0; color: #333; }}
        h2 {{ color: #444; border-bottom: 2px solid #e0e0e0; padding-bottom: 10px; margin-top: 30px; }}
        h3 {{ color: #555; }}
        .metadata {{
            background: #f8f9fa;
            padding: 20px;
            border-radius: 6px;
            margin-bottom: 30px;
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 15px;
        }}
        .metadata-item {{ display: flex; flex-direction: column; }}
        .metadata-label {{ font-size: 0.85em; color: #666; margin-bottom: 4px; }}
        .metadata-value {{ font-weight: 600; font-size: 1.1em; color: #333; }}
        .status-badge {{
            display: inline-block;
            padding: 6px 16px;
            border-radius: 4px;
            color: white;
            font-weight: bold;
            background: {status_color};
        }}
        .section {{
            margin: 20px 0;
            padding: 20px;
            background: #fafafa;
            border-radius: 6px;
            border-left: 4px solid #007bff;
        }}
        .quick-diagnosis {{
            background: #fff3cd;
            border-left-color: #ffc107;
            padding: 20px;
            border-radius: 6px;
        }}
        .quick-diagnosis h3 {{ margin-top: 0; color: #856404; }}
        .quick-diagnosis ul {{ margin: 10px 0; padding-left: 20px; }}
        .quick-diagnosis li {{ margin: 8px 0; }}
        table {{
            width: 100%;
            border-collapse: collapse;
            margin: 15px 0;
            background: white;
        }}
        th, td {{
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid #dee2e6;
        }}
        th {{
            background: #e9ecef;
            font-weight: 600;
            color: #495057;
            position: sticky;
            top: 0;
            z-index: 10;
        }}
        tr:hover {{ background: #f8f9fa; }}
        .row-number {{
            color: #6c757d;
            font-weight: 600;
            width: 40px;
            text-align: center;
        }}
        .resource-name {{
            font-family: monospace;
            font-size: 0.95em;
        }}
        .badge {{
            display: inline-block;
            padding: 4px 8px;
            border-radius: 4px;
            font-size: 0.85em;
            font-weight: 600;
        }}
        .badge-healthy {{
            background: #d4edda;
            color: #155724;
        }}
        .badge-unhealthy {{
            background: #f8d7da;
            color: #721c24;
        }}
        .truncation-note {{
            color: #6c757d;
            font-style: italic;
            font-size: 0.9em;
            margin-top: 10px;
        }}
        .pod-files-row td {{
            padding: 0 12px 12px 12px !important;
            background: #f8f9fa;
            border-bottom: 2px solid #dee2e6;
        }}
        .pod-files-detail {{
            display: flex;
            flex-wrap: wrap;
            gap: 4px;
        }}
        .file-tag {{
            display: inline-block;
            background: #e9ecef;
            padding: 2px 8px;
            border-radius: 3px;
            font-size: 0.8em;
            color: #495057;
        }}
        .healthy {{ color: #28a745; }}
        .unhealthy {{ color: #dc3545; }}
        .error-text {{ color: #dc3545; font-family: monospace; font-size: 0.9em; }}
        .error-pattern {{
            margin: 15px 0;
            padding: 15px;
            border-radius: 6px;
            border-left: 4px solid #dc3545;
        }}
        .error-pattern.critical {{ background: #f8d7da; border-left-color: #dc3545; }}
        .error-pattern.error {{ background: #fff3cd; border-left-color: #ffc107; }}
        .error-pattern.warning {{ background: #d1ecf1; border-left-color: #17a2b8; }}
        .error-pattern h4 {{ margin-top: 0; }}
        .copy-button {{
            background: #007bff;
            color: white;
            border: none;
            padding: 10px 20px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 14px;
            margin: 10px 5px 10px 0;
        }}
        .copy-button:hover {{ background: #0056b3; }}
        .collapsible {{
            cursor: pointer;
            user-select: none;
        }}
        .collapsible::before {{
            content: '▼ ';
            display: inline-block;
            transition: transform 0.2s;
        }}
        .collapsible.collapsed::before {{
            transform: rotate(-90deg);
        }}
        .collapsible-content {{
            display: block;
            overflow: hidden;
            transition: max-height 0.3s ease;
        }}
        .collapsible.collapsed + .collapsible-content {{
            display: none;
        }}
        pre {{
            background: #f4f4f4;
            padding: 15px;
            border-radius: 4px;
            overflow-x: auto;
            font-size: 0.85em;
        }}
        .timeline-entry {{
            padding: 10px 12px;
            margin: 2px 0;
            border-left: 4px solid #dee2e6;
            background: white;
            border-radius: 0 4px 4px 0;
            display: flex;
            align-items: start;
            gap: 10px;
            transition: background 0.15s;
        }}
        .timeline-entry:hover {{
            background: #f8f9fa;
        }}
        .timeline-entry.warning {{ border-left-color: #ffc107; }}
        .timeline-entry.badge-warning {{ border-left-color: #ffc107; }}
        .timeline-entry.error {{ border-left-color: #dc3545; }}
        .timeline-entry.badge-error {{ border-left-color: #dc3545; }}
        .timeline-entry.critical {{ border-left-color: #721c24; background: #f8d7da; }}
        .timeline-number {{
            flex-shrink: 0;
            min-width: 30px;
            color: #adb5bd;
            font-size: 0.8em;
            text-align: right;
            padding-top: 2px;
            font-variant-numeric: tabular-nums;
        }}
        .timeline-content {{
            flex: 1;
            min-width: 0;
        }}
        .timeline-header {{
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            align-items: center;
            font-size: 0.85em;
            margin-bottom: 4px;
        }}
        .timeline-message {{
            color: #333;
            font-size: 0.95em;
            word-wrap: break-word;
        }}
        .timeline-message-full {{
            color: #333;
            font-size: 0.95em;
            word-wrap: break-word;
            white-space: pre-wrap;
            background: #f8f9fa;
            padding: 8px;
            border-radius: 4px;
            margin-top: 4px;
        }}
        .timeline-timestamp {{
            color: #6c757d;
            font-family: monospace;
            font-size: 0.95em;
            cursor: help;
        }}
        .timeline-resource {{
            font-family: monospace;
            font-size: 0.95em;
            color: #495057;
        }}
        .event-reason {{
            display: inline-block;
            background: #e9ecef;
            color: #495057;
            padding: 1px 6px;
            border-radius: 3px;
            font-size: 0.95em;
            font-weight: 600;
        }}
        .badge-warning .event-reason {{
            background: #fff3cd;
            color: #856404;
        }}
        .badge-error .event-reason {{
            background: #f8d7da;
            color: #721c24;
        }}
        .event-count {{
            color: #6c757d;
            font-size: 0.9em;
        }}
        .artifact-group {{
            margin: 10px 0;
        }}
        .artifact-list {{
            list-style: none;
            padding-left: 0;
            margin: 5px 0;
        }}
        .artifact-list li {{
            padding: 3px 0;
            font-size: 0.9em;
            border-bottom: 1px solid #f0f0f0;
        }}
        .artifact-size {{
            color: #6c757d;
            font-size: 0.85em;
            margin-left: 8px;
        }}
        .artifacts-table td {{
            text-align: center;
        }}
        .artifacts-table td:first-child {{
            text-align: left;
        }}
        .empty-cell {{
            color: #dee2e6;
        }}
        .cluster-resources-grid {{
            display: flex;
            flex-wrap: wrap;
            gap: 6px;
            margin-bottom: 12px;
        }}
        .cluster-resources-group {{
            border-bottom: 1px solid #e9ecef;
            padding-bottom: 8px;
        }}
        .cluster-resources-group:last-child {{
            border-bottom: none;
        }}
        .log-modal {{
            position: fixed;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background: rgba(0,0,0,0.6);
            z-index: 1000;
            display: flex;
            align-items: center;
            justify-content: center;
        }}
        .log-modal-content {{
            background: #1e1e1e;
            border-radius: 8px;
            width: 90vw;
            max-width: 1200px;
            height: 80vh;
            display: flex;
            flex-direction: column;
            box-shadow: 0 8px 32px rgba(0,0,0,0.4);
        }}
        .log-modal-header {{
            display: flex;
            align-items: center;
            padding: 12px 16px;
            background: #2d2d2d;
            border-radius: 8px 8px 0 0;
            border-bottom: 1px solid #404040;
            gap: 12px;
        }}
        .log-modal-title {{
            color: #e0e0e0;
            font-family: monospace;
            font-size: 0.95em;
            font-weight: 600;
            flex: 1;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }}
        .log-modal-info {{
            color: #888;
            font-size: 0.85em;
            white-space: nowrap;
        }}
        .log-modal-close {{
            background: none;
            border: none;
            color: #888;
            font-size: 1.5em;
            cursor: pointer;
            padding: 0 4px;
            line-height: 1;
        }}
        .log-modal-close:hover {{
            color: #fff;
        }}
        .log-modal-body {{
            flex: 1;
            overflow: auto;
            margin: 0;
            padding: 16px;
            color: #d4d4d4;
            font-family: 'SF Mono', 'Menlo', 'Monaco', 'Courier New', monospace;
            font-size: 0.85em;
            line-height: 1.5;
            white-space: pre-wrap;
            word-wrap: break-word;
            background: #1e1e1e;
            border-radius: 0 0 8px 8px;
        }}
        .file-tag-link {{
            display: inline-block;
            background: #e9ecef;
            padding: 2px 8px;
            border-radius: 3px;
            font-size: 0.8em;
            color: #007bff;
            cursor: pointer;
            text-decoration: none;
        }}
        .file-tag-link:hover {{
            background: #007bff;
            color: #fff;
        }}
        .expand-link {{
            color: #007bff;
            cursor: pointer;
            text-decoration: underline;
            font-size: 0.9em;
            margin-left: 8px;
        }}
        .expand-link:hover {{
            color: #0056b3;
        }}
        .toggle-btn {{
            background: #6c757d;
            color: white;
            border: none;
            padding: 6px 12px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 0.85em;
            margin: 10px 0;
        }}
        .toggle-btn:hover {{
            background: #5a6268;
        }}
        .sample-errors {{
            margin-top: 10px;
            border-left: 2px solid #dee2e6;
            padding-left: 15px;
        }}
        .sample-error {{
            background: white;
            padding: 10px;
            margin: 8px 0;
            border-radius: 4px;
            border: 1px solid #dee2e6;
        }}
        .sample-error-header {{
            display: flex;
            justify-content: space-between;
            margin-bottom: 6px;
            font-size: 0.85em;
        }}
        .error-source {{
            font-family: monospace;
            color: #495057;
            font-weight: 600;
        }}
        .error-time {{
            color: #6c757d;
        }}
        .error-message {{
            font-family: monospace;
            font-size: 0.9em;
            color: #333;
            white-space: pre-wrap;
            word-wrap: break-word;
        }}
        .error-message-full {{
            font-family: monospace;
            font-size: 0.9em;
            color: #333;
            white-space: pre-wrap;
            word-wrap: break-word;
            margin-top: 8px;
            padding: 10px;
            background: #f8f9fa;
            border-radius: 4px;
        }}
        code {{
            background: #f8f9fa;
            padding: 2px 6px;
            border-radius: 3px;
            font-size: 0.9em;
        }}
        .failed-test {{
            background: white;
            padding: 15px;
            margin: 12px 0;
            border-radius: 4px;
            border-left: 4px solid #dc3545;
        }}
        .failed-test-header {{
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 8px;
        }}
        .test-name {{
            color: #dc3545;
            font-size: 1.05em;
        }}
        .test-duration {{
            color: #6c757d;
            font-size: 0.9em;
            font-weight: normal;
        }}
        .test-error {{
            font-family: monospace;
            font-size: 0.9em;
            color: #721c24;
            background: #f8d7da;
            padding: 10px;
            border-radius: 4px;
            white-space: pre-wrap;
            word-wrap: break-word;
            margin: 8px 0;
        }}
        .test-error-full {{
            font-family: monospace;
            font-size: 0.9em;
            color: #721c24;
            background: #f8d7da;
            padding: 10px;
            border-radius: 4px;
            white-space: pre-wrap;
            word-wrap: break-word;
            margin: 8px 0;
        }}
        .test-location {{
            font-family: monospace;
            font-size: 0.85em;
            color: #6c757d;
            margin-top: 6px;
        }}
    </style>
</head>
<body>
    <div class="container">
        <h1>E2E Test Summary</h1>

        <div class="metadata">
            <div class="metadata-item">
                <span class="metadata-label">Status</span>
                <span class="metadata-value"><span class="status-badge">{status}</span></span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Test Name</span>
                <span class="metadata-value">{escape(test_name)}</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Variant</span>
                <span class="metadata-value">{escape(variant)}</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Duration</span>
                <span class="metadata-value">{data['test_run'].get('duration_seconds', 0):.0f}s</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Tests</span>
                <span class="metadata-value">{total_tests} total ({failed_tests} failed)</span>
            </div>
            <div class="metadata-item">
                <span class="metadata-label">Generated</span>
                <span class="metadata-value">{data['meta']['generated_at'][:19]}</span>
            </div>
        </div>

        <div class="quick-diagnosis">
            <h3>Quick Diagnosis</h3>
            <ul>
                <li><strong>Test Status:</strong> {status} ({failed_tests}/{total_tests} tests failed)</li>
                <li><strong>Unhealthy Pods:</strong> {unhealthy_pods}</li>
                <li><strong>Total Errors:</strong> {len(data['errors'])} errors detected across {len(data['error_patterns'])} patterns</li>
                <li><strong>Topology:</strong> {data['topology']['type']}</li>
            </ul>
        </div>

        <button class="copy-button" onclick="copyJSON()">📋 Copy JSON Data</button>
        <button class="copy-button" onclick="downloadJSON()">💾 Download JSON</button>

        {failed_tests_html}

        {error_catalog_html}

        {resource_html}

        <h2 class="collapsible" onclick="toggleCollapse(this)">Timeline ({len(data['timeline'])} events)</h2>
        <div class="collapsible-content section">
            <div style="max-height: 600px; overflow-y: auto;">
                {''.join([_render_timeline_entry(idx, entry) for idx, entry in enumerate(data['timeline'][:200])])}
            </div>
            {f"<p class='truncation-note'>Showing first 200 of {len(data['timeline'])} events.</p>" if len(data['timeline']) > 200 else ''}
        </div>

        <h2 class="collapsible" onclick="toggleCollapse(this)">Artifacts ({data['artifacts']['summary']['total_files']} files, {data['artifacts']['summary']['total_size_bytes'] / 1024 / 1024:.1f} MB)</h2>
        <div class="collapsible-content section">
            <p><strong>By type:</strong> {', '.join([f"{k}: {v}" for k, v in sorted(data['artifacts']['summary']['by_type'].items())])}</p>
            {_render_artifacts_by_cluster(data)}
        </div>

        <h2 class="collapsible collapsed" onclick="toggleCollapse(this)">Full JSON Data</h2>
        <div class="collapsible-content section">
            <pre id="json-display">{escaped_json}</pre>
        </div>
    </div>

    <!-- Log viewer modal -->
    <div id="log-modal" class="log-modal" style="display:none;" onclick="if(event.target===this)closeLogModal()">
        <div class="log-modal-content">
            <div class="log-modal-header">
                <span id="log-modal-title" class="log-modal-title"></span>
                <span id="log-modal-info" class="log-modal-info"></span>
                <button class="log-modal-close" onclick="closeLogModal()">&times;</button>
            </div>
            <pre id="log-modal-body" class="log-modal-body"></pre>
        </div>
    </div>

    <script id="test-data" type="application/json">
{json_data}
    </script>

    <script id="log-tails-data" type="application/json">
{log_tails_json}
    </script>

    <script>
        function toggleCollapse(element) {{
            element.classList.toggle('collapsed');
        }}

        function toggleSamples(id) {{
            const element = document.getElementById(id);
            const button = event.target;
            if (element.style.display === 'none') {{
                element.style.display = 'block';
                button.textContent = button.textContent.replace('▼', '▲').replace('Show', 'Hide');
            }} else {{
                element.style.display = 'none';
                button.textContent = button.textContent.replace('▲', '▼').replace('Hide', 'Show');
            }}
        }}

        function toggleExpand(element) {{
            const parent = element.closest('.sample-error');
            const short = element.closest('.error-message');
            const full = parent.querySelector('.error-message-full');

            if (full.style.display === 'none') {{
                full.style.display = 'block';
                short.style.display = 'none';
            }} else {{
                full.style.display = 'none';
                short.style.display = 'block';
            }}
        }}

        function togglePodFiles(id) {{
            const row = document.getElementById(id);
            row.style.display = row.style.display === 'none' ? 'table-row' : 'none';
        }}

        function expandTimelineMsg(element) {{
            const short = element.closest('.timeline-message');
            const full = short.nextElementSibling;
            if (full && full.classList.contains('timeline-message-full')) {{
                if (full.style.display === 'none') {{
                    full.style.display = 'block';
                    short.style.display = 'none';
                }} else {{
                    full.style.display = 'none';
                    short.style.display = 'block';
                }}
            }}
        }}

        function toggleTestError(id) {{
            const fullError = document.getElementById(id);
            const shortError = fullError.previousElementSibling;
            const expandLink = shortError.querySelector('.expand-link');

            if (fullError.style.display === 'none') {{
                fullError.style.display = 'block';
                shortError.style.display = 'none';
            }} else {{
                fullError.style.display = 'none';
                shortError.style.display = 'block';
            }}
        }}

        function copyJSON() {{
            const jsonData = document.getElementById('test-data').textContent;
            navigator.clipboard.writeText(jsonData).then(() => {{
                alert('JSON data copied to clipboard!');
            }}).catch(err => {{
                console.error('Failed to copy:', err);
            }});
        }}

        function downloadJSON() {{
            const jsonData = document.getElementById('test-data').textContent;
            const blob = new Blob([jsonData], {{ type: 'application/json' }});
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = 'test-summary.json';
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        }}

        // Log viewer modal
        const _logTails = JSON.parse(document.getElementById('log-tails-data').textContent);

        function openLogModal(filename) {{
            const entry = _logTails[filename];
            if (!entry) {{
                alert('Log content not available for: ' + filename);
                return;
            }}

            const modal = document.getElementById('log-modal');
            const title = document.getElementById('log-modal-title');
            const info = document.getElementById('log-modal-info');
            const body = document.getElementById('log-modal-body');

            // Show just the short filename in the title
            const shortName = filename.includes('_') ? filename.split('_').slice(-1)[0] || filename : filename;
            title.textContent = shortName;
            title.title = filename;

            if (entry.truncated) {{
                const dir = entry.mode === 'head' ? 'first' : 'last';
                info.textContent = dir + ' ' + entry.shown_lines + ' of ' + entry.total_lines + ' lines';
            }} else {{
                info.textContent = entry.total_lines + ' lines';
            }}

            body.textContent = entry.content;
            modal.style.display = 'flex';
            document.body.style.overflow = 'hidden';

            // Logs: scroll to bottom (most recent). Structured files: scroll to top.
            requestAnimationFrame(() => {{
                body.scrollTop = entry.mode === 'head' ? 0 : body.scrollHeight;
            }});
        }}

        function closeLogModal() {{
            document.getElementById('log-modal').style.display = 'none';
            document.body.style.overflow = '';
        }}

        document.addEventListener('keydown', (e) => {{
            if (e.key === 'Escape') closeLogModal();
        }});

        // Auto-collapse long sections on load
        window.addEventListener('load', () => {{
            console.log('Test summary loaded. JSON data available in #test-data element.');
        }});
    </script>
</body>
</html>"""

    return html


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description='Generate comprehensive HTML test summary with embedded JSON'
    )
    parser.add_argument('logs_dir', nargs='?', default='logs',
                        help='Directory containing test artifacts')
    parser.add_argument('--output', '-o', default='logs/test-summary.html',
                        help='Output HTML file path')
    parser.add_argument('--test-name', default='[TEST_NAME]',
                        help='Test name')
    parser.add_argument('--variant', default='[VARIANT]',
                        help='Test variant')
    parser.add_argument('--status', default='[STATUS]',
                        choices=['PASSED', 'FAILED', '[STATUS]'],
                        help='Test status')

    args = parser.parse_args()

    logs_dir = Path(args.logs_dir)
    output_path = Path(args.output)

    if not logs_dir.exists():
        print(f"Error: Directory {logs_dir} does not exist", file=sys.stderr)
        sys.exit(1)

    # Generate summary data
    generator = TestSummaryGenerator(logs_dir)
    data = generator.generate()

    # Update metadata with command-line args
    data['meta']['test_type'] = args.test_name
    data['meta']['variant'] = args.variant

    # Generate HTML
    print("Generating HTML output...", file=sys.stderr)
    html_content = generate_html(data, args.test_name, args.variant, args.status)

    # Write output
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(html_content)

    print(f"✅ Generated: {output_path}", file=sys.stderr)
    print(f"   Total files processed: {data['artifacts']['summary']['total_files']}", file=sys.stderr)
    print(f"   Errors detected: {len(data['errors'])}", file=sys.stderr)
    print(f"   Error patterns: {len(data['error_patterns'])}", file=sys.stderr)
    print(f"   HTML size: {len(html_content) / 1024:.1f} KB", file=sys.stderr)


if __name__ == "__main__":
    main()
