---
title: subresourceEnabled Helm value was removed
kind: other
date: 2025-07-15
---

* `subresourceEnabled` Helm value was removed. This setting used to be `true` by default and made it possible to exclude subresource permissions from the operator role by specifying `false` as the value. We are removing this configuration option, making the operator roles always have subresource permissions. This setting was introduced as a temporary solution for [this](https://bugzilla.redhat.com/show_bug.cgi?id=1803171) OpenShift issue. The issue has since been resolved and the setting is no longer needed.
