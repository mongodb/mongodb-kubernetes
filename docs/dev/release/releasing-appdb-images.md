## Release new AppDB image to ECR
1. Update `release.json`: change `appDbImageAgentVersion` if new Agent binary should be included into the image,
change `appDbBundle` fields to use a new version of bundled MongoDB.
1. Invoke the manual Evergreen patch to release the image to ECR:
```bash
evergreen patch --project ops-manager-kubernetes -v build_and_push_appdb_images -d "AppDB release to ECR" --finalize
```
1. After the patch is finished create a PR for the changes in `release.json`

## Release new AppDB image to quay.io/redhat

1. Ensure that the PR with changes in `release.json` (related to appdb changes) is merged to master and tests
   are green. This will guarantee that the Operator to be released will work with this new appdb image.
1. Invoke (this should be replaced by the Evergreen patch using pipeline)
```bash
# login to quay.io
docker pull 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubuntu/mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent
docker tag 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubuntu/mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent quay.io/mongodb/mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent
docker push quay.io/mongodb/mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent

docker pull 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent
docker tag 268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent quay.io/mongodb/mongodb-enterprise-appdb-ubi:10.2.15.5958-1_4.2.11-ent
docker push quay.io/mongodb/mongodb-enterprise-appdb-ubi:10.2.15.5958-1_4.2.11-ent

```
