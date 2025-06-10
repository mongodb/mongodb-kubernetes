from pathlib import Path

import pygit2
import pytest


@pytest.fixture
def testrepo(tmp_path):
    with TemporaryRepository('testrepo.zip', tmp_path) as path:
        yield pygit2.Repository(path)


class TemporaryRepository:
    def __init__(self, name, tmp_path):
        self.name = name
        self.tmp_path = tmp_path

    def __enter__(self):
        path = Path(__file__).parent / 'data' / self.name
        temp_repo_path = Path(self.tmp_path) / path.stem
        if path.suffix == '.zip':
            with zipfile.ZipFile(path) as zipf:
                zipf.extractall(self.tmp_path)
        elif path.suffix == '.git':
            shutil.copytree(path, temp_repo_path)
        else:
            raise ValueError(f'Unexpected {path.suffix} extension')

        return temp_repo_path

    def __exit__(self, exc_type, exc_value, traceback):
        pass
