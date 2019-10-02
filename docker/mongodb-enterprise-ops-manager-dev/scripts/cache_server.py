#!/usr/bin/env python

"""This small server will download the MMS and MongoDB packages needed
to build the Ops Manager Docker Image. It needs to be called from the
Makefile, on its own terminal window:

$ make cache_server

Then, in a second terminal, the `build_cached` target from the
Makefile can be called:

$ make build_cached

This build process won't download the packages from the Internet, even
when the layers have been invalidaded.

"""

import http.server
import socketserver
import os
from os.path import dirname, join

from six.moves import urllib


PORT = 8000

Handler = http.server.SimpleHTTPRequestHandler

CACHE_DIR = join(dirname(dirname(__file__)), "cache")
OM_DOWNLOAD_LOCATION = "https://downloads.mongodb.com/on-prem-mms/deb"
MDB_DOWNLOAD_LOCATION = "https://fastdl.mongodb.org/linux"


def download(url, filename):
    output_file = join(CACHE_DIR, filename)
    if os.path.exists(output_file):
        return

    print('Downloading {}'.format(url))

    urllib.request.urlretrieve(url, output_file)


def download_mms():
    version = os.getenv("IMAGE_VERSION")
    filename = "mongodb-mms_{}_x86_64.deb".format(version)
    url = '{}/{}'.format(OM_DOWNLOAD_LOCATION, filename)
    download(url, filename)


def download_mongodb():
    version = os.getenv("MONGODB_VERSION", "4.0.0")
    filename = "mongodb-linux-x86_64-debian92-{}.tgz".format(version)
    url = "{}/{}".format(MDB_DOWNLOAD_LOCATION, filename)
    download(url, filename)


if __name__ == '__main__':
    if not os.path.exists(CACHE_DIR):
        os.makedirs(CACHE_DIR)

    download_mms()
    download_mongodb()

    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        print("serving at port", PORT)
        httpd.serve_forever()
