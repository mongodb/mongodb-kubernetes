import os

import pytest

current_dir = os.path.dirname(os.path.abspath(__file__))
test_dir = os.path.join(current_dir)
os.chdir(test_dir)

pytest.main()
