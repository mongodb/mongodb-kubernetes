#!/usr/bin/env bash

set -Eeou pipefail

title=$(gh pr view --json title | jq -r '.title')

if [[ "$title" == "[ticket]"* ]]; then
  echo "Title starts with '[ticket]', creating a Jira ticket..."

  title=${title#"[ticket]"}
  title=$(echo "$title" | xargs)
  echo "Title: $title"

  labels=$(gh pr view --json labels | jq -r '.labels | map(.name) | join(",")')
  echo "Labels: $labels"

  jira_output=$(jira issue create -tStory -s "$title" -C "Kubernetes Enterprise" --custom assigned-teams="Kubernetes Hosted" -b "This ticket was created from the attached PR" --priority "Minor - P4" --label "$labels" --no-input --raw)
  echo "Jira ticket created successfully."

  jira_id=$(echo "$jira_output" | jq -r '.key')

  gh pr edit --title "$jira_id $title"
  echo "PR title updated to include Jira ticket ID: $jira_id $title"
else
  echo "Title does not start with '[ticket]', skipping Jira ticket creation."
fi
