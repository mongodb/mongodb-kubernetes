import logging
import os
import sys

LOGLEVEL = os.environ.get("LOGLEVEL", "DEBUG").upper()

# Create handlers: all levels to stdout (so pytest/IntelliJ capture shows errors), WARNING+ also to stderr
# They are attached to each logger below
stdout_handler = logging.StreamHandler(sys.stdout)
stdout_handler.setLevel(logging.DEBUG)
# No filter on stdout so ERROR/WARNING are visible in captured output when running under pytest (e.g. in IntelliJ)
stderr_handler = logging.StreamHandler(sys.stderr)
stderr_handler.setLevel(logging.WARNING)

# Format the logs
formatter = logging.Formatter("%(levelname)-8s %(asctime)s [%(module)s]  %(message)s")
stdout_handler.setFormatter(formatter)
stderr_handler.setFormatter(formatter)


def get_test_logger(name: str) -> logging.Logger:
    logger = logging.getLogger(name)
    logger.setLevel(LOGLEVEL)
    logger.propagate = False
    logger.addHandler(stdout_handler)
    logger.addHandler(stderr_handler)
    return logger
