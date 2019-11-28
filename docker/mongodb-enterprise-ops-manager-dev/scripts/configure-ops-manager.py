#!/usr/bin/env python
"""
Configures a running instance of Ops Manager by:
- registering a global admin
- adding a default whitelist
- creating a project
- retrieving the public API key

After registration, it will save the generated credentials into ENV_FILE

Usage: {filename} OPS_MANAGER_HOST ENV_FILE
"""

import fileinput
import json
import urllib
from urllib import request
from os.path import basename, exists, dirname
import os
import sys

import docopt

# Replace current filename in docopt
__doc__ = __doc__.format(filename=basename(__file__))

DEFAULT_ADMIN = "admin"
DEFAULT_PASS = "admin12345%"


def post(om_url, data, username=None, token=None):
    data = bytes(json.dumps(data), encoding="utf-8")
    req = request.Request(om_url, data)
    req.add_header("Content-Type", "application/json")

    # Use Digest auth
    if username and token:
        # Create password manager
        password_mgr = urllib.request.HTTPPasswordMgrWithDefaultRealm()
        password_mgr.add_password(None, om_url, username, token)

        # Init the auth handler
        handler = urllib.request.HTTPDigestAuthHandler(password_mgr)
        opener = urllib.request.build_opener(handler)
        urllib.request.install_opener(opener)

    print("Ops Manager request: url: {}".format(om_url))
    resp = request.urlopen(req)
    return json.loads(resp.read().decode("utf-8"))


def main() -> int:
    # Retrieve arguments
    args = docopt.docopt(__doc__)
    url = args["OPS_MANAGER_HOST"].rstrip("/")
    filename = args["ENV_FILE"]

    # Internal Ops Manager hostname used by automation agents
    om_host = "export OM_HOST={}".format(url)

    # If the env vars have already been configured (global admin was registered)
    if exists(filename):
        print()
        print("# existing environment variables")
        output = ""
        for line in fileinput.input(filename, inplace=True):
            # Replace the OM_HOST value, if changed
            if "OM_HOST" in line and line != om_host:
                line = om_host + "\n"
            sys.stdout.write(line)
            output += line
        fileinput.close()
        print(output)

        # Stop here, as the global admin cannot be registered more than once
        return 0

    # Create first user (global owner)
    # using 0.0.0.0/1 and 128.0.0.0/1 for whitelist as /0 is blacklisted in Ops Manager
    user_data = post(
        url
        + (
            "/api/public/v1.0/unauth/users?"
            "whitelist=0.0.0.0%2F1&whitelist=128.0.0.0%2F1"
        ),
        {
            "username": DEFAULT_ADMIN,
            "password": DEFAULT_PASS,
            "firstName": "Admin",
            "lastName": "Admin",
        },
    )

    # Retrieve API key
    api_key = user_data["apiKey"]

    dir_name = dirname(args["ENV_FILE"])
    if not os.path.exists(dir_name):
        os.makedirs(dir_name)

    # Store env variables
    om_user = "export OM_USER={}".format(DEFAULT_ADMIN)
    om_pass = "export OM_PASSWORD={}".format(DEFAULT_PASS)
    om_api_key = "export OM_API_KEY={}".format(api_key)
    with open(args["ENV_FILE"], "w") as f:
        f.write(om_host + "\n")
        f.write(om_user + "\n")
        f.write(om_pass + "\n")
        f.write(om_api_key + "\n")

    # Also print them for immediate usage
    help_msg = f"""
Ops Manager was configured and the environment was saved at: {filename}
You can import it with:
    'eval "$(docker exec ops_manager cat {filename})" # Docker
    'eval "$(kubectl -n mongodb exec mongodb-enterprise-ops-manager-0 cat {filename})" # Kubernetes

DON'T FORGET TO CHANGE THE DEFAULT PASSWORD AND ROTATE THE PUBLIC API KEY, IF RUNNING IN A PRODUCTION ENVIRONMENT!

    """
    print(help_msg)
    return 0


if __name__ == "__main__":
    sys.exit(main())
