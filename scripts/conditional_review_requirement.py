import json
import sys
from pathlib import Path
from typing import Any

EXTRA_REVIEW_LABEL = "needs-extra-review"
REQUIRED_APPROVALS = 2


def _current_approval_count(reviews: list[dict[str, Any]], author: str) -> int:
    latest_states = {}
    ordered_reviews = sorted(
        reviews,
        key=lambda review: (review.get("submitted_at") or "", review.get("id", 0)),
    )

    for review in ordered_reviews:
        if not review.get("submitted_at"):
            continue

        user = review.get("user") or {}
        login = user.get("login")
        if user.get("type") != "User" or not login or login.casefold() == author.casefold():
            continue

        latest_states[login.casefold()] = review.get("state")

    return sum(state == "APPROVED" for state in latest_states.values())


def review_requirement_status(pull_request: dict[str, Any], reviews: list[dict[str, Any]]) -> tuple[str, str]:
    labels = {label.get("name", "").casefold() for label in pull_request.get("labels", [])}
    if EXTRA_REVIEW_LABEL.casefold() not in labels:
        return "success", f"{EXTRA_REVIEW_LABEL} is not set; the default one-approval rule applies."

    author = pull_request["user"]["login"]
    approvals = _current_approval_count(reviews, author)
    if approvals >= REQUIRED_APPROVALS:
        return "success", f"{approvals} distinct reviewers approved; {REQUIRED_APPROVALS} required."

    return "failure", f"{approvals} of {REQUIRED_APPROVALS} distinct reviewer approvals."


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("Usage: conditional_review_requirement.py PULL_REQUEST_JSON REVIEW_PAGES_JSON")

    pull_request = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
    review_pages = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"))
    reviews = [review for page in review_pages for review in page]
    state, description = review_requirement_status(pull_request, reviews)
    print(json.dumps({"state": state, "description": description}))


if __name__ == "__main__":
    main()
