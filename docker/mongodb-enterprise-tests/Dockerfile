FROM python:3.6-alpine

WORKDIR /tests
RUN mkdir -p /tests/fixtures
COPY requirements.txt /tests/

RUN python3 -m pip install -r requirements.txt

COPY fixtures/* /tests/fixtures/
COPY kubetester.py test*.py /tests/

