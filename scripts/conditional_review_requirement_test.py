from scripts.conditional_review_requirement import review_requirement_status


def pull_request(labels=(), author="author"):
    return {
        "labels": [{"name": label} for label in labels],
        "user": {"login": author},
    }


def review(login, state, submitted_at, user_type="User"):
    return {
        "id": int(submitted_at[-3:-1]),
        "state": state,
        "submitted_at": submitted_at,
        "user": {"login": login, "type": user_type},
    }


def test_default_review_requirement_applies_without_extra_review_label():
    state, _ = review_requirement_status(pull_request(), [])

    assert state == "success"


def test_extra_review_label_requires_two_distinct_approvals():
    pr = pull_request(["needs-extra-review"])
    one_approval = [review("reviewer-one", "APPROVED", "2025-01-01T00:00:01Z")]
    two_approvals = one_approval + [review("reviewer-two", "APPROVED", "2025-01-01T00:00:02Z")]

    assert review_requirement_status(pr, one_approval)[0] == "failure"
    assert review_requirement_status(pr, two_approvals)[0] == "success"


def test_only_latest_submitted_human_reviews_count():
    pr = pull_request(["needs-extra-review"])
    reviews = [
        review("reviewer-one", "APPROVED", "2025-01-01T00:00:01Z"),
        review("reviewer-one", "COMMENTED", "2025-01-01T00:00:02Z"),
        review("reviewer-two", "APPROVED", "2025-01-01T00:00:03Z"),
        review("author", "APPROVED", "2025-01-01T00:00:04Z"),
        review("review-bot", "APPROVED", "2025-01-01T00:00:05Z", user_type="Bot"),
    ]

    assert review_requirement_status(pr, reviews)[0] == "failure"
