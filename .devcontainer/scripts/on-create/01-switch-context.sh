#!/bin/bash

set -euo pipefail

# Switch context once, to kickstart the environment
context=root-context
if [ -f "/workspace/.generated/.current_context" ]; then
    context=$(cat /workspace/.generated/.current_context)
fi
make switch context="${context}"
