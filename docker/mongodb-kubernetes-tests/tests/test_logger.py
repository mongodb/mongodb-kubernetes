import logging
import os

LOGLEVEL = os.environ.get("LOGLEVEL", "DEBUG").upper()


def get_test_logger(name: str) -> logging.Logger:
    logger = logging.getLogger(name)
    logger.setLevel(LOGLEVEL)
    return logger
