#!/usr/bin/env python3
"""Generate comprehensive HTML test summary with embedded JSON for E2E test artifacts.

This tool processes E2E test artifacts (logs, YAML dumps, events) and generates a single
HTML file containing both structured JSON data (for AI agents) and an interactive UI (for humans).
"""

import argparse
import sys
from pathlib import Path

from collector import TestSummaryGenerator
from renderer import generate_html


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(description="Generate comprehensive HTML test summary with embedded JSON")
    parser.add_argument("logs_dir", nargs="?", default="logs", help="Directory containing test artifacts")
    parser.add_argument("--output", "-o", default="logs/test-summary.html", help="Output HTML file path")
    parser.add_argument("--test-name", default="[TEST_NAME]", help="Test name")
    parser.add_argument("--variant", default="[VARIANT]", help="Test variant")
    parser.add_argument("--status", default="[STATUS]", choices=["PASSED", "FAILED", "[STATUS]"], help="Test status")

    args = parser.parse_args()

    logs_dir = Path(args.logs_dir)
    output_path = Path(args.output)

    if not logs_dir.exists():
        print(f"Error: Directory {logs_dir} does not exist", file=sys.stderr)
        sys.exit(1)

    # Generate summary data
    generator = TestSummaryGenerator(logs_dir)
    data = generator.generate()

    # Update metadata with command-line args
    data["meta"]["test_type"] = args.test_name
    data["meta"]["variant"] = args.variant

    # Generate HTML
    print("Generating HTML output...", file=sys.stderr)
    html_content = generate_html(data, args.test_name, args.variant, args.status)

    # Write output
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(html_content)

    print(f"\u2705 Generated: {output_path}", file=sys.stderr)
    print(f"   Total files processed: {data['artifacts']['summary']['total_files']}", file=sys.stderr)
    print(f"   Errors detected: {len(data['errors'])}", file=sys.stderr)
    print(f"   Error patterns: {len(data['error_patterns'])}", file=sys.stderr)
    print(f"   HTML size: {len(html_content) / 1024:.1f} KB", file=sys.stderr)


if __name__ == "__main__":
    main()
