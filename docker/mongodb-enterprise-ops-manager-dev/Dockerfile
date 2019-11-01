# This is a Docker file for Ops Manager 4.0
FROM debian:9.5-slim

# Environment declaration
ENV OM_PORT 8080
EXPOSE ${OM_PORT}
ENV mongodb /opt/mongodb/bin
ENV log_dir /data/logs
ENV om_log_dir /opt/mongodb/mms/logs
ENV mms_prop_file /opt/mongodb/mms/conf/conf-mms.properties
ENV mms_conf_file /opt/mongodb/mms/conf/mms.conf

ARG MONGODB_VERSION
ARG OPS_MANAGER_VERSION
ARG OM_DOWNLOAD_LOCATION
ARG MDB_DOWNLOAD_LOCATION

# Non-interactive setup
RUN echo 'debconf debconf/frontend select Noninteractive' | debconf-set-selections

# Enable the testing repo (for Python3.7)
RUN echo "deb http://ftp.de.debian.org/debian testing main" >> /etc/apt/sources.list \
    && echo 'APT::Default-Release "oldstable";' >> /etc/apt/apt.conf.d/00local

# Install system depedencies
# Structured according to: https://docs.docker.com/develop/develop-images/dockerfile_best-practices/#run
RUN apt-get -qq update \
    && apt-get -y -qq install \
        apt-utils \
        ca-certificates \
        curl \
        libsasl2-2 \
        net-tools \
        netcat \
        procps \
        sudo \
        libnss-wrapper \
    && apt-get -qq -yt testing install \
        python3-pip \
        python3.7-venv \
        python3.7 \
    && apt-get upgrade -y -qq \
    && apt-get dist-upgrade -y -qq \
    && rm -rf /var/lib/apt/lists/*

# Download and install Ops Manager, set apropriate permissions
WORKDIR /opt
ENV ops_manager_deb mongodb-mms_${OPS_MANAGER_VERSION}_x86_64.deb

# gets the mms download file from localhost (need a way to copy this into the image)
RUN curl -o "${ops_manager_deb}" "${OM_DOWNLOAD_LOCATION}/${ops_manager_deb}" \
    && dpkg -i "${ops_manager_deb}" \
    && rm "${ops_manager_deb}"  \
    && chown -R mongodb-mms:root /opt/mongodb/mms \
    && chmod -R ug+w /opt/mongodb/mms

# clean the apt repo
RUN apt-get autoclean -y || true

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
ENV mongodb_name mongodb-linux-x86_64-debian92-${MONGODB_VERSION}

RUN curl -o "${mongodb_name}.tgz" "${MDB_DOWNLOAD_LOCATION}/${mongodb_name}.tgz" \
    && tar xzf "${mongodb_name}.tgz" \
    && rm "${mongodb_name}.tgz" \
    && mv "${mongodb_name}"/* mongodb/

# Copy scripts
COPY scripts/ /opt/scripts
RUN chown -R mongodb-mms:root /opt/scripts \
    && chmod -R 0775 /opt/scripts

# Prepare a Python3 environment, for running the setup script (user registration, project, whitelist)
RUN python3.7 -m venv /opt/venv \
    && . /opt/venv/bin/activate \
    && python -m pip install --upgrade pip \
    && python -m pip install -r "/opt/scripts/requirements.txt" \
    && chown -R mongodb-mms:mongodb-mms /opt/venv \
    && chmod -R 775 /opt/venv

RUN chown mongodb-mms:root /etc/mongodb-mms \
    && chmod 0770 /etc/mongodb-mms

RUN mkdir -p /data \
	&& chmod 775 /data

# Explicitly declare the volumes that are to be mounted (Docker) or provided via Persistent Volumes (Kubernetes)
VOLUME ["/etc/mongodb-mms", "/data" ]

CMD /opt/scripts/entrypoint.sh
