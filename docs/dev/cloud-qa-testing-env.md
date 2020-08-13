# Cloud-QA

Cloud-qa (https://cloud-qa.mongodb.com) is a few days ahead of Cloud Manager
(and Atlas) releases. We use this environment to run our tests against an
always-running Cloud environment.

## Getting Credentials for Cloud-QA

The different Ops Manager instances that run inside the Kubernetes
Cluster will use a `GLOBAL_OWNER` user. This is different as to how we
connect "Cloud-qa". In this case, the user is created manually and the
credentials are configured in the `ops-manager-kubernetes` Evergreen
project. These credentials will work for 30 days (until the trial
expires). To create a new user:

* Visit [Cloud QA Registration
  Page](https://cloud-qa.mongodb.com/user#/cloud/register/accountProfile)
* Register a new user with the following data:

| attribute | value | notes |
|-----------|-------|-------|
| Email Address | `ops-manager-team+cloud-qa-kube-operator-e2e-<index>@mongodb.com` | Make sure you increment the `<index>` |
| Password | *Ask someone on the Kubernetes team about this password.*| |
| First Name | Kubernetes | |
| Last Name | E2E Tests | |
| Phone Number | +353 (01) 901 4654 | This is the Dublin Office Phone Number |
| Company Name | Ireland | |
| Job Function | DBA | |
| Country | Ireland | |

* After logging in, go to "Billing" and add the a fake credit card. You'll find
  the details in
  [here](https://wiki.corp.mongodb.com/display/MMS/MMS+Test+Plan+-+Billing#MMSTestPlan-Billing-B.BillingSettings).
  
## Creating a Programmatic API Key for Tests

The `cloud-qa` tests work by generating a programmatic API Key on each test run,
based on a "master" Programmatic API Key that you'll have to create manually:

* Go to Access
* Click on "Manage" and then "Create API Key"
* Write down the "Public Key" (something like: zgjgujkc)
* Add a description (like: E2E Tests Runner)
* In "Organization Permissions" choose "Organization Owner"
* Write down the "Private Key" (something like: f8ed33fc-9e60-44e7-b9d8-60a61e02ebd6)
* Add the following IP to the "allowed list":

  - 0.0.0.0/1
  - 128.0.0.0/1
* Get the Organization ID for this organization (from the URL)
* Finally, update this information into [Evergreen
  project](https://evergreen.mongodb.com/projects##ops-manager-kubernetes).

* The attributes to complete are:
  - `e2e_cloud_qa_apikey_owner`: The new programmatic API Key private part
  - `e2e_cloud_qa_baseurl`: This is always
    `https://cloud-qa.mongodb.com`
  - `e2e_cloud_qa_orgid_owner` : Organization ID
  - `e2e_cloud_qa_user_owner` : The new programmatic API Key public part

## Errors

### NO\_FREE\_TIER\_API

Sometimes you'll see this reported on the tests. If this happens, you might want
to login to cloud-qa and re-enter your fake credit card details, as described in
one of the previous sections.

### Reaching the 250 project limit

Each test running on cloud-qa will create a programmatic api key, based on the
configured "global" api key. At the end of the test, any residuals should be
removed, but sometimes they stay in the organization. When this happens, a new
"Organization" needs to be created. To fix this, you'll have to create a new org
and point the Evergreen project to use that instead:

* Go to: https://cloud-qa.mongodb.com/v2#/preferences/organizations
* Click on create new organization
* Create a cloud-manager org, name it Kubernetes Enterprise Operator E2E - \<index\> (no need to add members)
* Now with the org create, go to access manager
* Follow the steps to create a new programmatic api key (above in this doc)
