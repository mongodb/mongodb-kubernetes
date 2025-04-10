import logging
import subprocess
import tempfile
import time
from dataclasses import dataclass
from html.parser import HTMLParser

import requests


@dataclass(init=True)
class QueryableBackupParams:
    host: str
    ca_pem: str
    client_pem: str


class CsrfHtmlParser(HTMLParser):
    def parse_html(self, html):
        self._csrf_fields = dict({})
        self.feed(html)
        return self._csrf_fields

    def handle_starttag(self, tag, attrs):
        if tag == "meta":
            attr_name = next((a[1] for a in attrs if a[0] == "name"), None)
            attr_content = next((a[1] for a in attrs if a[0] == "content"), None)
            if attr_name is not None and attr_name.startswith("csrf"):
                self._csrf_fields[attr_name] = attr_content


class OMQueryableBackup:
    def __init__(self, om_url, project_id):
        self._om_url = om_url
        self._project_id = project_id

    def _login(self):
        endpoint = f"{self._om_url}/user/v1/auth"
        headers = {"Content-Type": "application/json"}
        data = {
            "username": "jane.doe@example.com",
            "password": "Passw0rd.",
            "reCaptchaResponse": None,
        }

        response = requests.post(endpoint, json=data, headers=headers)
        if response.status_code != 200:
            raise Exception(f"OM login failed with status code: {response.status_code}, content: {response.content}")

        self._auth_cookies = response.cookies

    def _authenticated_http_get(self, url, headers=None):
        response = requests.get(url, headers=headers or {}, cookies=self._auth_cookies)
        if response.status_code != 200:
            raise Exception(f"HTTP GET failed with status code: {response.status_code}, content: {response.content}")
        return response

    def _get_snapshots_query_host(self):
        return (
            self._authenticated_http_get(
                f"{self._om_url}/v2/{self._project_id}/params",
                headers={"Accept": "application/json"},
            )
            .json()
            .get("snapshotsQueryHost")
        )

    def _get_first_snapshot_id(self):
        return (
            self._authenticated_http_get(
                f"{self._om_url}/backup/web/snapshot/{self._project_id}/mdb-four-two",
                headers={"Accept": "application/json"},
            )
            .json()
            .get("entries")[0]
            .get("id")
        )

    def _get_csrf_headers(self):
        html_response = self._authenticated_http_get(f"{self._om_url}/v2/{self._project_id}").text
        csrf_fields = CsrfHtmlParser().parse_html(html_response)
        return {f"x-{k}": v for k, v in csrf_fields.items()}

    def _start_query_backup(self, first_snapshot_id, csrf_headers):
        response = requests.put(
            f"{self._om_url}/backup/web/restore/{self._project_id}/query/{first_snapshot_id}",
            headers=csrf_headers,
            cookies=self._auth_cookies,
        )
        return response.json().get("snapshotQueryId")

    def _download_client_cert(self, snapshot_query_id):
        return self._authenticated_http_get(
            f"{self._om_url}/backup/web/restore/{self._project_id}/query/{snapshot_query_id}/keypair"
        ).text

    def _download_ca(self):
        return self._authenticated_http_get(f"{self._om_url}/backup/web/restore/{self._project_id}/query/ca").text

    def _wait_until_ready_to_query(self, timeout: int):
        initial_timeout = timeout
        ready_statuses = ["waitingForCustomer", "completed"]
        last_reached_state = ""
        while timeout > 0:
            restore_entries = self._authenticated_http_get(
                f"{self._om_url}/v2/backup/restore/{self._project_id}",
                headers={"Accept": "application/json"},
            ).json()

            if len(restore_entries) > 0 and restore_entries[0].get("progressPhase") in ready_statuses:
                time_needed = initial_timeout - timeout
                print(f"needed {time_needed} seconds to be able to query backups")
                return
            last_reached_state = restore_entries[0].get("progressPhase")
            time.sleep(3)
            timeout -= 3

        raise Exception(
            f"Timeout ({initial_timeout}) reached while waiting for '{ready_statuses}' snapshot query status. "
            f"Last reached status: {last_reached_state}"
        )

    def connection_params(self, timeout: int):
        """Retrieves the connection config (host, ca / client pem files) used to query a backup snapshot."""
        self._login()

        first_snapshot_id = self._get_first_snapshot_id()

        csrf_headers = self._get_csrf_headers()

        snapshot_query_id = self._start_query_backup(first_snapshot_id, csrf_headers)

        self._wait_until_ready_to_query(timeout)

        return QueryableBackupParams(
            host=self._get_snapshots_query_host(),
            ca_pem=self._download_ca(),
            client_pem=self._download_client_cert(snapshot_query_id),
        )


def generate_queryable_pem(namespace: str):
    # todo: investigate if cert-manager can be used instead of openssl
    openssl_conf = f"""
prompt=no
distinguished_name = qb_req_distinguished_name

x509_extensions = qb_extensions

[qb_req_distinguished_name]
C=US
ST=New York
L=New York
O=MongoDB, Inc.
CN=queryable-backup-test.mongodb.com

[qb_extensions]
basicConstraints=CA:true
subjectAltName=@qb_subject_alt_names

[qb_subject_alt_names]
DNS.1 = om-backup-svc.{namespace}.svc.cluster.local    
"""

    openssl_conf_file = tempfile.NamedTemporaryFile(delete=False, mode="w")
    openssl_conf_file.write(openssl_conf)
    openssl_conf_file.flush()

    csr_file_path = "/tmp/queryable-backup.csr"
    key_file_path = "/tmp/queryable-backup.key"

    args = [
        "openssl",
        "req",
        "-new",
        "-x509",
        "-days",
        "824",
        "-nodes",
        "-out",
        csr_file_path,
        "-newkey",
        "rsa:2048",
        "-keyout",
        key_file_path,
        "-config",
        openssl_conf_file.name,
    ]

    try:
        completed_process = subprocess.run(args, capture_output=True)
        completed_process.check_returncode()
    except subprocess.CalledProcessError as exc:
        stdout = exc.stdout.decode("utf-8")
        stderr = exc.stderr.decode("utf-8")
        logging.info(stdout)
        logging.info(stderr)
        raise

    with open(csr_file_path, "r") as csr_file, open(key_file_path, "r") as key_file:
        return f"{csr_file.read()}\n{key_file.read()}"
