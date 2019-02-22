#!/usr/bin/env python3

# is_older_than.py
#
# Usage:
#
#   ./is_older_than.py <datetime> <amount> <unit>
#
# Exits with a successful exit code if <datetime> is older than <amount> <unit>s of time.
# The format of <datetime> is a simple iso datetime as returned by
#
#   kubectl get namespaces -o jsonpath='{.items[*].metadata.creationTimestamp}'
#
# Example:
#
#  1. Check if a given timestamp is older than 6 hours
#   ./is_older_than.py 2019-02-22T11:28:04Z 6 hours
#
#  2. Check if a given timestamp is older than 3 days
#   ./is_older_than.py 2019-02-22T11:28:04Z 3 days
#
#  3. Check if Rodrigo is older than 39 years.
#     This command will return 1 until my next birthday.
#   ./is_older_than.py 1980-25-04T11:00:04Z 39 years
#

from datetime import datetime, timedelta
import sys

if __name__ == "__main__":
    # parses the date as first argument
    date = datetime.strptime(sys.argv[1], '%Y-%m-%dT%H:%M:%SZ')

    # gets the following options, same as we use to construct the timedelta object,
    # like 'minutes 6' -- it is expected in command line as '6 minutes'
    delta_options = {sys.argv[3]: int(sys.argv[2])}
    delta = timedelta(**delta_options)

    sys.exit(datetime.now() - delta > date)
