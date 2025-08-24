#!/usr/bin/env python3

import os
import sys
from collections import defaultdict


def find_snippet_directories():
    """Find all directories containing both test.sh and code_snippets subdirectory."""
    snippet_dirs = []

    # Traverse current directory recursively to find test.sh files
    for root, dirs, files in os.walk("."):
        if "test.sh" in files:
            # Check if this directory also has a code_snippets subdirectory
            code_snippets_path = os.path.join(root, "code_snippets")
            if os.path.isdir(code_snippets_path):
                snippet_dirs.append(root)

    return snippet_dirs


def verify_snippets_files_are_unique():
    """Check if files in snippet directories have unique names across all directories."""
    dirs = find_snippet_directories()

    if not dirs:
        print("No snippet directories found (no test.sh files).")
        return True

    file_map = defaultdict(list)

    print(f"Checking for duplicate file names across code snippet directories:\n {"\t\n".join(dirs)}")

    # Scan all files in code_snippets subdirectories only
    for snippet_dir in dirs:
        code_snippets_dir = os.path.join(snippet_dir, "code_snippets")
        if os.path.exists(code_snippets_dir):
            for file in os.listdir(code_snippets_dir):
                file_path = os.path.join(code_snippets_dir, file)
                if os.path.isfile(file_path):
                    file_map[file].append(file_path)

    # Check for duplicates
    duplicates_found = False
    for filename, paths in file_map.items():
        if len(paths) > 1:
            if not duplicates_found:
                print("ERROR: Duplicate file names found:")
                duplicates_found = True
            print(f"  File '{filename}' appears in multiple locations:")
            for path in sorted(paths):
                print(f"    {path}")
            print()

    if duplicates_found:
        print("Please rename duplicate files to ensure uniqueness across all snippet directories.")
        return False
    else:
        print("All snippet files have unique names across directories.")
        return True


if __name__ == "__main__":
    checks = [verify_snippets_files_are_unique()]

    # Exit 0 if all checks pass, 1 if any fail
    sys.exit(0 if all(checks) else 1)
