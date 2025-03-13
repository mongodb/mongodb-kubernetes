#!/bin/bash

 set -eou pipefail

 git config --global --add safe.directory /github/workspace

 commit_single_file() {
   # Commit to the branch
   FILE_TO_COMMIT="$1"
   SHA=$(git rev-parse "$DESTINATION_BRANCH:$FILE_TO_COMMIT") || true
   MESSAGE="Pushing $FILE_TO_COMMIT using GitHub API"

   echo "$DESTINATION_BRANCH:$FILE_TO_COMMIT:$SHA"
   if [ "$SHA" = "$DESTINATION_BRANCH:$FILE_TO_COMMIT" ]; then
       echo "File does not exist"
       gh api --method PUT /repos/:owner/:repo/contents/"$FILE_TO_COMMIT" \
        --field message="$MESSAGE" \
        --field content=@<( base64 -i "$FILE_TO_COMMIT" ) \
        --field branch="$DESTINATION_BRANCH"
       echo "$FILE_TO_COMMIT pushed to $DESTINATION_BRANCH"
   else
       echo "File exists"
        gh api --method PUT /repos/:owner/:repo/contents/"$FILE_TO_COMMIT" \
               --field message="$MESSAGE" \
               --field content=@<( base64 -i "$FILE_TO_COMMIT" ) \
               --field branch="$DESTINATION_BRANCH" \
               --field sha="$SHA"
       echo "$FILE_TO_COMMIT pushed to $DESTINATION_BRANCH"
   fi
 }

 # simple 'for loop' does not work correctly, see https://github.com/koalaman/shellcheck/wiki/SC2044#correct-code
 while IFS= read -r -d '' file
 do
   commit_single_file "$file"
 done <   <(find "${PATH_TO_COMMIT}" -type f -print0)
