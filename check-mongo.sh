kubectl delete -f samples/node-mongo-app.yaml; kubectl apply -f samples/node-mongo-app.yaml
sleep 2
kubectl logs -l app=test-mongo-app
