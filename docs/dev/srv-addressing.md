# Test SRV Addressing

### Prerequisites

- Add `python-pip` as a dependency to the mongodb-enterprise-database Dockerfile
- Set up an Ops Manager instance using `om-evg`
- Rebuild the image with `make`

### Test Steps

- Deploy a mongod with `kubectl apply -f public/samples/minimal/standalone.yaml`
- Connect to a the mongod `kubectl exec -it my-standalone-0 bash`
- Install the python dependencies `pip install pymongo && pip install dnspython`
- Take the script from [here](https://github.com/jasonmimick/simple-mongodb-connection-tester-container/blob/master/simple-connection-test.py) (Thank you Jason Mimick)
- Make sure to change the `uri` variable to `mongodb+srv://my-standalone-svc.mongodb.svc.cluster.local/?ssl=false`

SRV addressing is working correctly if the script executes with no errors.

