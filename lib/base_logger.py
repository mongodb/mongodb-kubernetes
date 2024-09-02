import logging
import os
import sys

LOGLEVEL = os.environ.get("LOGLEVEL", "DEBUG").upper()

# Create handlers to output Debug and Info logs to stdout, and above to stderr
# They are attached to each logger below
stdout_handler = logging.StreamHandler(sys.stdout)
stdout_handler.setLevel(logging.DEBUG)
stdout_handler.addFilter(lambda record: record.levelno <= logging.INFO)
stderr_handler = logging.StreamHandler(sys.stderr)
stderr_handler.setLevel(logging.WARNING)

# Format the logs
formatter = logging.Formatter("%(levelname)-8s %(asctime)s [%(module)s]  %(message)s")
stdout_handler.setFormatter(formatter)
stderr_handler.setFormatter(formatter)

# Pipeline logger
logger = logging.getLogger("pipeline")
logger.setLevel(LOGLEVEL)
logger.propagate = False
logger.addHandler(stdout_handler)
logger.addHandler(stderr_handler)

# Sonar logger
sonar_logger = logging.getLogger("sonar")
sonar_logger.setLevel(LOGLEVEL)
sonar_logger.propagate = False
sonar_logger.addHandler(stdout_handler)
sonar_logger.addHandler(stderr_handler)
