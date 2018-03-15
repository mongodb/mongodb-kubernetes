FROM ubuntu

RUN apt-get -qq update && apt-get -y -qq install libsasl2-2 curl supervisor

COPY content/ /mongodb-automation/
RUN mkdir -p /var/log/mongodb-mms-automation

# CMD /mongodb-automation/agent.sh
CMD supervisord -c /mongodb-automation/supervisor.conf

# To build: docker build . -t ops-manager-agent

# To run normally:
# docker run -t ops-manager-agent -e BASE_URL="base_url" -e GROUP_ID="group_id" -e AGENT_API_KEY="your-key"

# To run with server pools:
# docker run -t ops-manager-agent -e BASE_URL="base_url" -e SERVER_POOL_KEY="server_pool_key"
