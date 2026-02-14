"""Shared constants for search tests.

This module contains common constants used across all search test files
to avoid duplication and ensure consistency.
"""

# =============================================================================
# User Credentials
# =============================================================================

# Admin user - has full privileges for database administration
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

# Mongot sync user - used by mongot for data synchronization
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

# Regular user - has limited privileges for search operations
USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

# =============================================================================
# Ports
# =============================================================================

# Default mongot gRPC port
MONGOT_PORT = 27028

# Envoy proxy port (used for external LB configurations)
ENVOY_PROXY_PORT = 27029

# Envoy admin port
ENVOY_ADMIN_PORT = 9901

# =============================================================================
# Sample Data
# =============================================================================

# URL for the sample_mflix database archive used in search tests
SAMPLE_MFLIX_ARCHIVE_URL = "https://atlas-education.s3.amazonaws.com/sample_mflix.archive"

# Database and collection names for sample_mflix
SAMPLE_MFLIX_DB = "sample_mflix"
SAMPLE_MFLIX_MOVIES_COLLECTION = "movies"
SAMPLE_MFLIX_EMBEDDED_MOVIES_COLLECTION = "embedded_movies"
