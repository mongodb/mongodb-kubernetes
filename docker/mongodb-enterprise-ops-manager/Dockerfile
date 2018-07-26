# This is a Docker file for Ops Manager 4.0
FROM debian:9.4-slim

# Environment declaration
ENV OM_PORT 8080
EXPOSE ${OM_PORT}
ENV mongodb /opt/mongodb/bin
ENV log_dir /data/logs
ENV mms_prop_file /opt/mongodb/mms/conf/conf-mms.properties
ENV mms_conf_file /opt/mongodb/mms/conf/mms.conf

ARG MONGODB_VERSION
ARG OPS_MANAGER_VERSION

# Non-interactive setup
RUN echo 'debconf debconf/frontend select Noninteractive' | debconf-set-selections

# Enable the testing repo (for Python3.6)
RUN echo "deb http://ftp.de.debian.org/debian testing main" >> /etc/apt/sources.list \
    && echo 'APT::Default-Release "stable";' >> /etc/apt/apt.conf.d/00local

# Install system depedencies
RUN apt-get -qq update \
    && apt-get -y -qq install apt-utils libsasl2-2 curl ca-certificates procps net-tools sudo netcat \
    && apt-get -qq -yt testing install python3.6 python3-venv \
    && apt-get upgrade -y -qq \
    && apt-get dist-upgrade -y -qq \
    && rm -rf /var/lib/apt/lists/*

# Download and install Ops Manager, set apropriate permissions
WORKDIR /opt
RUN curl -o mms.deb https://downloads.mongodb.com/on-prem-mms/deb/mongodb-mms_${OPS_MANAGER_VERSION}_x86_64.deb?utm_source=docker \
    && dpkg -i mms.deb \
    && rm mms.deb \
    && chown -R mongodb-mms:mongodb-mms /opt/mongodb/mms \
    && chmod -R 0775 /opt/mongodb/mms

# Set the minimum options required for Ops Manager start-up and enable the health endpoint
RUN echo "mms.ignoreInitialUiSetup=true"                            >> ${mms_prop_file} \
    && echo "rootDirectory=/data/backupDaemon/"                     >> ${mms_prop_file} \
    && echo "mms.fromEmailAddr=ops-manager-no-reply@example.com"    >> ${mms_prop_file} \
    && echo "mms.adminEmailAddr=ops-manager-no-reply@example.com"   >> ${mms_prop_file} \
    && echo "mms.replyToEmailAddr=ops-manager-no-reply@example.com" >> ${mms_prop_file} \
    && echo "mms.mail.transport=smtp"                               >> ${mms_prop_file} \
    && echo "mms.mail.hostname=localhost"                           >> ${mms_prop_file} \
    && echo "mms.mail.port=25"                                      >> ${mms_prop_file}

# Download MongoDB
WORKDIR /opt
RUN curl -o mongodb.tgz https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-debian92-${MONGODB_VERSION}.tgz?utm_source=docker \
    && tar xzf mongodb.tgz \
    && rm mongodb.tgz \
    && mv mongodb-linux-x86_64-debian92-${MONGODB_VERSION}/* mongodb/ \
    && chown -R mongodb-mms:mongodb-mms ${mongodb} \
    && chmod -R 0775 ${mongodb}

# Copy scripts
COPY scripts/ /opt/scripts
RUN chown -R mongodb-mms:mongodb-mms /opt/scripts \
    && chmod -R 0775 /opt/scripts

# Prepare a Python3 environment, for running the setup script (user registration, project, whitelist)
RUN python3.6 -m venv /opt/venv \
    && . /opt/venv/bin/activate \
    && python -m pip install --upgrade pip \
    && python -m pip install -r "/opt/scripts/requirements.txt" \
    && chown -R mongodb-mms:mongodb-mms /opt/venv \
    && chmod -R 775 /opt/venv

# Create the required directories and set their perms
RUN mkdir -p /data \
    && chown mongodb-mms:mongodb-mms /data \
    && chmod 0770 /data \
    && chown mongodb-mms:mongodb-mms /etc/mongodb-mms \
    && chmod 0770 /etc/mongodb-mms

# Openshift: allow the root group to update users and groups at runtime
RUN chmod g+w /etc/passwd /etc/group

# Explicitly declare the volumes that are to be mounted (Docker) or provided via Persistent Volumes (Kubernetes)
VOLUME ["/etc/mongodb-mms", "/data"]

# Run the container
USER mongodb-mms
CMD /opt/scripts/entrypoint.sh
