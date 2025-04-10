#!/usr/bin/env bash

# This script is used in evergreen yaml for dynamically adding task to be executed in given variant.

variant=$1
task=$2

if [[ -z "${variant}" || -z "${task}" ]]; then
  echo "usage: add_evergreen_task.sh {variant} {task}"
  exit 1
fi

cat >evergreen_tasks.json <<EOF
{
    "buildvariants": [
        {
            "name": "${variant}",
            "tasks": [
              {
                "name": "${task}"
              }
            ]
        }
    ]
}
EOF

echo "Generated tasks for evergreen to add:"
cat evergreen_tasks.json
