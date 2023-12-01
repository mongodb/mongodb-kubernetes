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

import sys
from datetime import datetime, timedelta


def is_older_than(date, amount, unit):
    """Checks if datetime is older than `amount` of `unit`"""
    date = datetime.strptime(date, "%Y-%m-%dT%H:%M:%SZ")
    # gets the following options, same as we use to construct the timedelta object,
    # like 'minutes 6' -- it is expected in command line as '6 minutes'
    delta_options = {unit: amount}
    delta = timedelta(**delta_options)

    return date + delta > datetime.now()


if __name__ == "__main__":
    date = sys.argv[1]
    amount = int(sys.argv[2])
    unit = sys.argv[3]

    sys.exit(is_older_than(date, amount, unit))
