FROM debian:jessie-slim

RUN groupadd -r mongodb && useradd -r -g mongodb mongodb

RUN apt-get -qq update \
    && apt-get -y -qq install libsasl2-2 curl supervisor \
    && rm -rf /var/lib/apt/lists/*

COPY content/ /mongodb-automation/
RUN mkdir -p /var/log/mongodb-mms-automation \
    && mkdir -p /var/lib/mongodb-mms-automation \
    && mkdir -p /data \
    && chown mongodb /var/log/mongodb-mms-automation \
    && chown mongodb /var/lib/mongodb-mms-automation \
    && chown mongodb /data \
    && chown mongodb /mongodb-automation

CMD supervisord -c /mongodb-automation/supervisor.conf

# To build: docker build . -t ops-manager-agent

# To run normally (should specify env variables first):
# docker run -it ops-manager-agent
