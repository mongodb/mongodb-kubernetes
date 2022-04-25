# Backup S3 SnapshotStore through IRSA

On request from a customer, details in ticket [CLOUDP-93545](https://jira.mongodb.org/browse/CLOUDP-93545), we have implemented 
support for AWS SnapshotStore through IRSA. More details about IRSA can be found [here](https://aws.amazon.com/blogs/opensource/introducing-fine-grained-iam-roles-service-accounts/).


## Setting up OM S3 SnapshotStore with IRSA in EKS

#### Prerequisites
* Provision cluster in EKS
    ```bash
    eksctl create cluster irptest
    ```


* Create OIDC ID provider on EKS:
    ```bash
    eksctl utils associate-iam-oidc-provider \
    --name irptest \
    --approve
    ```
* Set the target bucket name used for the snapshot
   ```bash
   export TARGET_BUCKET=irp-test-2023
   ```
* Create S3 bucket in AWS to be used as Snapshot store.
   ```bash
   aws s3api create-bucket \
        --bucket $TARGET_BUCKET \
        --create-bucket-configuration LocationConstraint=$(aws configure get region) \
        --region $(aws configure get region)
   ```

* Create ops-manager Service Account, with IRSA annotation
  ```bash
  eksctl create iamserviceaccount \
        --name mongodb-enterprise-ops-manager \
        --override-existing-serviceaccounts \
        --namespace raj \
        --cluster irptest1 \
        --attach-policy-arn arn:aws:iam::aws:policy/AmazonS3FullAccess \
        --approve
  ```

*Note: the `attach-policy-arn` needs to be `arn:aws:iam::aws:policy/AmazonS3FullAccess` , i.e giving full S3 access to the Role*

* Deploy the enterprise-operator
* Deploy the Ops-Manager CR YAML file, make sure to set the following fields:
  Add `irsaEnabled` to backup spec
  ```
  spec:
    backup:
      s3Stores:
        - name: s3Store1
          ....
          irsaEnabled: true
  ```
  Add OM app settings `brs.store.s3.iam.flavor` in the OM yaml file:
  ```
  spec:
    configuration:
      ...
      ...
      ...
      brs.store.s3.iam.flavor: web-identity-token
  ```
  Note: You'll also need to set some value under `s3SecretRef.name` to pass CR validation. But this value will be ignored, since IAM role is used to configure auth.
-------------------------------------------------------------------------------------------------

  ***Dev Note: To run this test locally, ensure you have an EKS cluster setup with OIDC provider and then run `make e2e test=e2e_om_ops_manager_backup_irsa`***