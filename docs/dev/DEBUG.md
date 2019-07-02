This doc is for people trying to determine the cause of failures related to the Operator


To tail the Operator logs:

    $ kubectl logs -f deployment/mongodb-enterprise-operator -n mongodb

To start the operator in debug mode:

    $ make debug
    
Once the operator is in debug mode, the port, connect your debugger to port `30042`

In order to connect successfully, you must ensure there is an Inbound `Custom TCP Rule` with the Port range `30042` from an appropriate source.

On AWS: `EC2 > Security Groups > Inbound > Edit`
