# Check database connectivity #

After new deployment is created it's always good to check whether it works correctly. To do this you can deploy a small
`node.js` application into Kubernetes cluster which will try to connect to database, create 3 records there and read all existing ones:

    $ eval $(minikube docker-env)
    $ docker build -t node-mongo-app:0.1 docker/node-mongo-app -f docker/node-mongo-app/Dockerfile
    ....
    Successfully tagged node-mongo-app:0.1

Now copy `samples/node-mongo-app.yaml` to `samples/my-node-mongo-app.yaml` and change the `DATABASE_URL` property in
`samples/node-mongo-app.yaml` to target the mongodb deployment.
This can be a single url (for standalone) or a list of replicas/mongos instances (e.g.
`mongodb://liffey-0.alpha-service:27017,liffey-1.alpha-service:27017,liffey-2.alpha-service:27017/?replicaSet=liffey` for replica set or
 `mongodb://shannon-mongos-0.shannon-svc.mongodb.svc.cluster.local:27017,shannon-mongos-1.shannon-svc.mongodb.svc.cluster.local:27017`
 for sharded cluster).
Hostnames can be received form OM deployment page and have the short form `<pod-name>.<service-name>` or full version
`<pod-name>.<service-name>.mongodb.svc.cluster.local`
After this create a job in Kubernetes (it will run once and terminate):

    $ kubectl delete -f samples/my-node-mongo-app.yaml; kubectl apply -f samples/my-node-mongo-app.yaml
    deployment "test-mongo-app" configured

Reading logs:

    $ kubectl logs -l app=test-mongo-app -n mongodb
    Connected successfully to server
    Collection deleted
    Inserted 3 documents into the collection
    Found the following records
    [ { _id: 5aabda6c9398583e26bf211a, a: 1 },
      { _id: 5aabda6c9398586118bf211b, a: 2 },
      { _id: 5aabda6c939858c046bf211c, a: 3 } ]
