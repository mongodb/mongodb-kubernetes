# Mongodb-Agent
The agent gets released in a matrix style with the init-database image, which gets tagged with the operator version.
This works by using the multi-stage pattern and build-args. First - retrieve the `init-database:<version>` and retrieve the 
binaries from there. Then we continue with the other steps to fully build the image.