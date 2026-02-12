"""HTML rendering for E2E test summary dashboard.

Builds section HTML from collected data and assembles the final single-file
HTML document with inlined CSS/JS from dashboard.css and dashboard.js.
"""

import json
from pathlib import Path
from html import escape
from typing import Dict, Any
from collections import defaultdict

# Load CSS and JS assets once from sibling files
_ASSETS_DIR = Path(__file__).parent
_CSS = (_ASSETS_DIR / 'dashboard.css').read_text()
_JS = (_ASSETS_DIR / 'dashboard.js').read_text()


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


def _render_failed_tests(data: Dict) -> str:
    """Render failed test details with expandable error messages."""
    failed_tests = data['test_run'].get('tests', {}).get('failed', 0)
    if failed_tests == 0:
        return ""

    html = "<h2>Failed Tests</h2><div class='section'>"
    for idx, test in enumerate(data['test_run'].get('tests', {}).get('details', [])):
        if test['status'] == 'failed':
            html += "<div class='failed-test'>"
            html += f"<div class='failed-test-header'>"
            html += f"<span class='test-name'><strong>{escape(test['name'])}</strong></span>"
            html += f"<span class='test-duration'>{test['duration']:.1f}s</span>"
            html += "</div>"
            error_msg = test.get('error_message', 'Unknown error')
            if len(error_msg) > 200:
                html += f"<div class='test-error'>{escape(error_msg[:200])}..."
                html += f"<span class='expand-link' onclick='toggleTestError(\"test-error-{idx}\")'>expand</span></div>"
                html += f"<div id='test-error-{idx}' class='test-error-full' style='display: none;'>{escape(error_msg)}</div>"
            else:
                html += f"<div class='test-error'>{escape(error_msg)}</div>"
            if test.get('file'):
                html += f"<div class='test-location'>{escape(test['file'])}"
                if test.get('line'):
                    html += f":{escape(test['line'])}"
                html += "</div>"
            html += "</div>"
    html += "</div>"
    return html


def _render_resource_inventory(data: Dict, log_tails: Dict) -> str:
    """Render resource inventory: pods, statefulsets, deployments, cluster resources."""
    html = "<h2>Resource Inventory</h2>"
    html += f"<div class='section'><h3>Pods ({len(data['resources']['pods'])})</h3>"
    if data['resources']['pods']:
        html += "<table><tr><th>#</th><th>Name</th><th>Cluster</th><th>Phase</th><th>Ready</th><th>Restarts</th><th>Logs</th><th>Agent</th></tr>"
        for idx, pod in enumerate(data['resources']['pods'][:50], 1):
            phase_class = 'healthy' if pod['phase'] == 'Running' else 'unhealthy'
            log_count = len(pod['files'].get('logs', []))
            agent_count = len(pod['files'].get('agent_logs', []))
            has_describe = '1' if pod['files'].get('describe') else '-'
            has_health = '1' if pod['files'].get('health') else '-'
            html += f"<tr class='{phase_class}' onclick='togglePodFiles(\"pod-files-{idx}\")' style='cursor: pointer;'>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(pod['name'])}</td>"
            html += f"<td>{escape(pod['cluster'])}</td>"
            html += f"<td><span class='badge badge-{phase_class}'>{escape(pod['phase'])}</span></td>"
            html += f"<td>{escape(pod['ready'])}</td>"
            html += f"<td>{pod['restarts']}</td>"
            html += f"<td>{log_count}</td>"
            html += f"<td>{agent_count}</td>"
            html += "</tr>"
            # Expandable file details row
            all_files = (pod['files'].get('logs', []) + pod['files'].get('agent_logs', [])
                         + pod['files'].get('config', []))
            if pod['files'].get('describe'):
                all_files.append(pod['files']['describe'])
            if pod['files'].get('health'):
                all_files.append(pod['files']['health'])
            html += f"<tr id='pod-files-{idx}' class='pod-files-row' style='display: none;'>"
            html += f"<td colspan='8'><div class='pod-files-detail'>"
            if all_files:
                for f in sorted(all_files):
                    file_label = f.split('_')[-1] if '_' in f else f
                    # Make viewable files clickable
                    if f in log_tails:
                        html += f"<code class='file-tag-link' onclick=\"event.stopPropagation(); openLogModal('{escape(f)}')\">{escape(file_label)}</code> "
                    else:
                        html += f"<code class='file-tag'>{escape(file_label)}</code> "
            else:
                html += "<span class='truncation-note'>No files found for this pod</span>"
            html += "</div></td></tr>"
        html += "</table>"
        if len(data['resources']['pods']) > 50:
            html += f"<p class='truncation-note'>Showing first 50 of {len(data['resources']['pods'])} pods.</p>"
    else:
        html += "<p>No pods found</p>"

    # StatefulSets
    html += f"<h3 style='margin-top: 20px;'>StatefulSets ({len(data['resources']['statefulsets'])})</h3>"
    if data['resources']['statefulsets']:
        html += "<table><tr><th>#</th><th>Name</th><th>Cluster</th><th>Replicas</th><th>Owner</th><th>YAML</th></tr>"
        for idx, sts in enumerate(data['resources']['statefulsets'][:50], 1):
            readiness_class = 'healthy' if sts['ready_replicas'] == sts['desired_replicas'] else 'unhealthy'
            yaml_file = sts.get('files', {}).get('yaml', '')
            html += f"<tr class='{readiness_class}'>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(sts['name'])}</td>"
            html += f"<td>{escape(sts['cluster'])}</td>"
            html += f"<td>{sts['ready_replicas']}/{sts['desired_replicas']}</td>"
            html += f"<td class='resource-name'>{escape(sts.get('owner_ref', '-') or '-')}</td>"
            if yaml_file and yaml_file in log_tails:
                html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(yaml_file)}')\">view</code></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"
        html += "</table>"
    else:
        html += "<p>No StatefulSets found</p>"

    # Deployments
    html += f"<h3 style='margin-top: 20px;'>Deployments ({len(data['resources']['deployments'])})</h3>"
    if data['resources']['deployments']:
        html += "<table><tr><th>#</th><th>Name</th><th>Cluster</th><th>Replicas</th><th>YAML</th></tr>"
        for idx, deploy in enumerate(data['resources']['deployments'][:50], 1):
            readiness_class = 'healthy' if deploy['ready_replicas'] == deploy['desired_replicas'] else 'unhealthy'
            yaml_file = deploy.get('files', {}).get('yaml', '')
            html += f"<tr class='{readiness_class}'>"
            html += f"<td class='row-number'>{idx}</td>"
            html += f"<td class='resource-name'>{escape(deploy['name'])}</td>"
            html += f"<td>{escape(deploy['cluster'])}</td>"
            html += f"<td>{deploy['ready_replicas']}/{deploy['desired_replicas']}</td>"
            if yaml_file and yaml_file in log_tails:
                html += f"<td><code class='file-tag-link' onclick=\"openLogModal('{escape(yaml_file)}')\">view</code></td>"
            else:
                html += "<td>-</td>"
            html += "</tr>"
        html += "</table>"
    else:
        html += "<p>No Deployments found</p>"

    # Cluster Resources - clickable links to all z_ dump files and other useful files
    html += _render_cluster_resources(data, log_tails)

    html += "</div>"
    return html


def _render_error_catalog(data: Dict) -> str:
    """Render error catalog with expandable sample errors."""
    top_errors = data['error_patterns'][:5]

    html = "<h2>Error Catalog</h2><div class='section'>"
    if top_errors:
        for idx, pattern in enumerate(top_errors):
            severity_class = pattern['severity']
            html += f"<div class='error-pattern {severity_class}'>"
            html += f"<h4>{escape(pattern['description'])} ({pattern['count']} occurrences)</h4>"
            html += f"<p><strong>Pattern:</strong> <code>{escape(pattern['pattern'])}</code></p>"
            html += f"<p><strong>Affected resources:</strong> {len(pattern['affected_resources'])}</p>"

            # Show sample errors
            sample_error_ids = pattern.get('error_ids', [])[:3]
            if sample_error_ids:
                html += f"<button class='toggle-btn' onclick='toggleSamples(\"samples-{idx}\")'>Show sample errors \u25bc</button>"
                html += f"<div id='samples-{idx}' class='sample-errors' style='display: none;'>"
                for error_id in sample_error_ids:
                    error = next((e for e in data['errors'] if e['id'] == error_id), None)
                    if error:
                        html += "<div class='sample-error'>"
                        html += f"<div class='sample-error-header'>"
                        html += f"<span class='error-source'>{escape(error['source'].get('file', 'unknown'))}:{error['source'].get('line', '?')}</span>"
                        if error.get('timestamp'):
                            html += f"<span class='error-time'>{escape(error['timestamp'])}</span>"
                        html += "</div>"
                        message = error.get('message', '')
                        if len(message) > 150:
                            html += f"<div class='error-message'>{escape(message[:150])}..."
                            html += f"<span class='expand-link' onclick='toggleExpand(this)'>expand</span></div>"
                            html += f"<div class='error-message-full' style='display: none;'>{escape(message)}</div>"
                        else:
                            html += f"<div class='error-message'>{escape(message)}</div>"
                        html += "</div>"
                html += "</div>"

            html += "</div>"
    else:
        html += "<p>No errors detected</p>"
    html += "</div>"
    return html


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

    status_color = "#28a745" if status == "PASSED" else "#dc3545" if status == "FAILED" else "#6c757d"

    # Build sections
    failed_tests_html = _render_failed_tests(data)
    error_catalog_html = _render_error_catalog(data)
    resource_html = _render_resource_inventory(data, log_tails)
    timeline_html = ''.join([_render_timeline_entry(idx, entry) for idx, entry in enumerate(data['timeline'][:200])])
    timeline_truncation = f"<p class='truncation-note'>Showing first 200 of {len(data['timeline'])} events.</p>" if len(data['timeline']) > 200 else ''

    html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>E2E Test Summary - {escape(test_name)}</title>
    <style>
{_CSS}    </style>
</head>
<body>
    <div class="container">
        <h1>E2E Test Summary</h1>

        <div class="metadata">
            <div class="metadata-item">
                <span class="metadata-label">Status</span>
                <span class="metadata-value"><span class="status-badge" style="--status-color: {status_color}">{status}</span></span>
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

        <button class="copy-button" onclick="copyJSON()">\U0001f4cb Copy JSON Data</button>
        <button class="copy-button" onclick="downloadJSON()">\U0001f4be Download JSON</button>

        {failed_tests_html}

        {error_catalog_html}

        {resource_html}

        <h2 class="collapsible" onclick="toggleCollapse(this)">Timeline ({len(data['timeline'])} events)</h2>
        <div class="collapsible-content section">
            <div style="max-height: 600px; overflow-y: auto;">
                {timeline_html}
            </div>
            {timeline_truncation}
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
{_JS}
    </script>
</body>
</html>"""

    return html
