#!/bin/bash
# Prints the first found local IP
read -ra LOCAL_IPS <<< "$(/sbin/ifconfig | grep -E 'inet[^0-9]+' | sed -E 's/.*inet[^0-9]+([0-9\.]+).*/\1/g' | grep -v 127.0.0.1)"
echo "${LOCAL_IPS[0]}"
