import argparse
import datetime
import os
import re

from scripts.release.changelog import (
    BREAKING_CHANGE_ENTRIES,
    BUGFIX_ENTRIES,
    DEFAULT_CHANGELOG_PATH,
    FEATURE_ENTRIES,
    FILENAME_DATE_FORMAT,
    FRONTMATTER_DATE_FORMAT,
    PRELUDE_ENTRIES,
    parse_change_date,
)

MAX_TITLE_LENGTH = 50


def sanitize_title(title: str) -> str:
    # Remove non-alphabetic and space characters
    regex = re.compile("[^a-zA-Z ]+")
    title = regex.sub("", title)

    # Lowercase and split by space
    words = [word.lower() for word in title.split(" ")]

    result = words[0]

    for word in words[1:]:
        if len(result) + len("_") + len(word) <= MAX_TITLE_LENGTH:
            result = result + "_" + word
        else:
            break

    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(formatter_class=argparse.RawTextHelpFormatter)
    parser.add_argument(
        "-c",
        "--changelog-path",
        default=DEFAULT_CHANGELOG_PATH,
        metavar="",
        action="store",
        type=str,
        help=f"Path to the changelog directory relative to the repository root. Default is {DEFAULT_CHANGELOG_PATH}",
    )
    parser.add_argument(
        "-d",
        "--date",
        default=datetime.datetime.now().strftime(FRONTMATTER_DATE_FORMAT),
        metavar="",
        action="store",
        type=str,
        help=f"Date in 'YYYY-MM-DD' format to use for the changelog entry. Default is today's date",
    )
    parser.add_argument(
        "-e",
        "--editor",
        action="store_true",
        help="Open the created changelog entry in the default editor (if set, otherwise uses 'vi'). Default is True",
    )
    parser.add_argument(
        "-k",
        "--kind",
        action="store",
        metavar="",
        required=True,
        type=str,
        help=f"""Kind of the changelog entry:
  - '{".".join(PRELUDE_ENTRIES)}' for prelude entries
  - '{".".join(BREAKING_CHANGE_ENTRIES)}' for breaking change entries
  - '{".".join(FEATURE_ENTRIES)}' for feature entries
  - '{".".join(BUGFIX_ENTRIES)}' for bugfix entries
  - everything else will be treated as other entries""",
    )
    parser.add_argument("title", type=str, help="Title for the changelog entry")
    args = parser.parse_args()

    date = parse_change_date(args.date, FRONTMATTER_DATE_FORMAT)
    sanitized_title = sanitize_title(args.title)
    filename = f"{datetime.datetime.strftime(date, FILENAME_DATE_FORMAT)}_{args.kind}_{sanitized_title}.md"

    working_dir = os.getcwd()
    changelog_path = os.path.join(working_dir, args.changelog_path, filename)

    # Create directory if it doesn't exist
    os.makedirs(os.path.dirname(changelog_path), exist_ok=True)

    # Create the file
    with open(changelog_path, "w") as f:
        # Add frontmatter based on args
        f.write("---\n")
        f.write(f"title: {args.title}\n")
        f.write(f"kind: {args.kind}\n")
        f.write(f"date: {date}\n")
        f.write("---\n\n")

    if args.editor:
        editor = os.environ.get("EDITOR", "vi")  # Fallback to vim if EDITOR is not set
        os.system(f'{editor} "{changelog_path}"')

    print(f"Created changelog entry at: {changelog_path}")
