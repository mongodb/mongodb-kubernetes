FROM python:3.6-alpine

RUN mkdir -p /tests/fixtures
COPY requirements.txt kubetester.py test*.py /tests/
COPY fixtures/* /tests/fixtures/

WORKDIR /tests
RUN python3 -m pip install -r requirements.txt
