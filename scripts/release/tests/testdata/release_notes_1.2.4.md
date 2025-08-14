# MCK 1.2.4 Release Notes

## Bug Fixes

* Fixes the bug when status of `MongoDBUser` was being set to `Updated` prematurely. For example, new users were not immediately usable following `MongoDBUser` creation despite the operator reporting `Updated` state.
