import os

from scripts.release.kubectl_mongodb import download_kubectl_plugin
from scripts.release.kubectl_mongodb.download_kubectl_plugin import (
    KUBECTL_MONGODB_PLUGIN_BIN_PATH,
    download_kubectl_plugin_from_s3,
)


class FakeS3Client:
    def download_file(self, bucket: str, key: str, local_path: str) -> None:
        with open(local_path, "w") as plugin:
            plugin.write("plugin binary")


def test_copy_to_bin_creates_a_missing_bin_directory(tmp_path, monkeypatch):
    workdir = tmp_path / "workdir"
    workdir.mkdir()
    monkeypatch.setenv("workdir", str(workdir))
    monkeypatch.setattr(download_kubectl_plugin, "create_s3_client", lambda: FakeS3Client())
    downloaded = str(tmp_path / "kubectl-mongodb_amd64")

    download_kubectl_plugin_from_s3("bucket", "key", downloaded, copy_to_bin_path=True)

    copied = workdir / KUBECTL_MONGODB_PLUGIN_BIN_PATH
    assert copied.read_text() == "plugin binary"
    assert os.access(copied, os.X_OK)


def test_copy_to_bin_keeps_an_existing_bin_directory(tmp_path, monkeypatch):
    workdir = tmp_path / "workdir"
    (workdir / "bin").mkdir(parents=True)
    (workdir / "bin" / "other-tool").write_text("kept")
    monkeypatch.setenv("workdir", str(workdir))
    monkeypatch.setattr(download_kubectl_plugin, "create_s3_client", lambda: FakeS3Client())
    downloaded = str(tmp_path / "kubectl-mongodb_amd64")

    download_kubectl_plugin_from_s3("bucket", "key", downloaded, copy_to_bin_path=True)

    assert (workdir / "bin" / "other-tool").read_text() == "kept"
    assert (workdir / KUBECTL_MONGODB_PLUGIN_BIN_PATH).read_text() == "plugin binary"
