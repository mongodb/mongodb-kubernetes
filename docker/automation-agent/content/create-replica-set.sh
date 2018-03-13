#!/bin/bash

# TODO: This code has to be moved to the actual Operator


# put this into background and hopefully it will run after
# the automation agent.
if [[ $(hostname | grep '\-0$') ]]; then

    # this is for 3 member replica set
    hostname=`hostname -f`
    member1=`echo $hostname | sed -e 's/-0\./-1./g'`
    member2=`echo $hostname | sed -e 's/-0\./-2./g'`

    sed -e "s/:HOSTNAME0:/$hostname/g" \
        -e "s/:HOSTNAME1:/$member1/g" \
        -e "s/:HOSTNAME2:/$member2/g" \
        -e "s/:MONGOD_VERSION:/$MONGOD_VERSION/g" \
        /mongodb-automation/automationConfigPatch.json > /mongodb-automation/payload.json


    echo "Configuring replica set in OM for $hostname"
    curl -u "$EMAIL:$PUBLIC_API_KEY" -H "Content-Type: application/json" "$BASE_URL/api/public/v1.0/groups/$GROUP_ID/automationConfig" --digest -i -X PUT --data-binary "@/mongodb-automation/payload.json"
fi
