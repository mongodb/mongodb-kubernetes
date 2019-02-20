# How to deploy an Ops Manager instance in AWS (EC2)

### Prerequisites

- Credentials for an AWS account
- [ego](https://github.com/10gen/mms/blob/master/scripts/ops_manager/ego)

### Spin up an EC2 instance in AWS
- Ubuntu 18.04 is a safe bet
- At least 16 GB of RAM
- Assign an elastic IP to the newly created AMI
- **Wait until the public DNS has changed, before running ego!**

### Deploy and configure Ops Manager

```bash
OPS_MANAGER_HOST=ubuntu@...
# This is OM 4.0.8; you can also use other versions, e.g.: https://info-mongodb-com.s3.amazonaws.com/com-download-center/ops_manager_latest.json
PACKAGE=https://s3.amazonaws.com/mongodb-mms-build-onprem/0dac6f313138aed7a06bcfd53e792b676c5b139d/mongodb-mms_4.0.8.50386.20190206T1708Z-1_x86_64.deb
./ego seed "${OPS_MANAGER_HOST}"
./ego nohup "${OPS_MANAGER_HOST}" bin/ego scenario_install_package_from_link "${PACKAGE}" "http://${OPS_MANAGER_HOST#*@}:9080"
./ego tail "${OPS_MANAGER_HOST}"
```

### NOTE about SMTP on the Ops Manager host

If you need the OM host to be able to send emails, go to:
- edit `/opt/mongodb/mms/conf/conf-mms.properties` on the host
- and update the email/SMTP settings to use the AWS credentials provided in [MMS](https://github.com/10gen/mms/blob/master/server/conf/conf-hosted-mms-e2e.properties#L15)
- then restart Ops Manager: `sudo systemctl restart mongodb-mms`
