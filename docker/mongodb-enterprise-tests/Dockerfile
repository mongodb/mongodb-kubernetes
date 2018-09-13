FROM python:3.6-alpine

RUN mkdir -p /tests/fixtures
COPY requirements.txt run.sh kubetester.py test*.py /tests/
COPY fixtures/* /tests/fixtures/

WORKDIR /tests
RUN python3 -m pip install -r requirements.txt

ADD https://storage.googleapis.com/kubernetes-release/release/v1.10.0/bin/linux/amd64/kubectl /bin/kubectl
RUN chmod +x /bin/kubectl

CMD ["/tests/run.sh"]
