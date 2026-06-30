# MCK Project Conventions

## Go test layout
- Tests first, helpers at the bottom of the file.
- Error expectations use a `wantErr string` field. If non-empty, assert `err != nil` and that the string is contained in `err.Error()`. The string must be an actual substring of the error message, not a description of intent. Never compare full error messages literally.
