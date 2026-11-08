import http.client
import json
import os
import sys
import time
import urllib.parse

BASE_URL = os.environ.get("SCRAPELOG_URL", "http://127.0.0.1:8080")
API_KEY = os.environ["SCRAPELOG_KEY"]
RECORDS_PER_SECOND = 200
CONNECTION_SECONDS = 60


def build_line(record_number):
    record = {
        "method": "GET",
        "url": f"https://example.com/stream/{record_number}",
        "status": 200,
        "latency_ms": 80 + record_number % 40,
        "scraper": "example-python-stream",
    }
    return (json.dumps(record) + "\n").encode("utf-8")


def send_chunk(connection, chunk):
    connection.send(f"{len(chunk):x}\r\n".encode("ascii") + chunk + b"\r\n")


def stream_one_connection(first_record_number):
    parsed_url = urllib.parse.urlparse(BASE_URL)
    connection_class = http.client.HTTPSConnection if parsed_url.scheme == "https" else http.client.HTTPConnection
    connection = connection_class(parsed_url.netloc, timeout=CONNECTION_SECONDS + 30)
    connection.putrequest("POST", "/v1/requests/stream")
    connection.putheader("Authorization", "Bearer " + API_KEY)
    connection.putheader("Content-Type", "application/x-ndjson")
    connection.putheader("Transfer-Encoding", "chunked")
    connection.endheaders()

    record_number = first_record_number
    deadline = time.monotonic() + CONNECTION_SECONDS
    while time.monotonic() < deadline:
        send_chunk(connection, build_line(record_number))
        record_number += 1
        time.sleep(1 / RECORDS_PER_SECOND)
    connection.send(b"0\r\n\r\n")

    response = connection.getresponse()
    print(response.status, response.read().decode("utf-8"), flush=True)
    connection.close()
    return record_number, response.status


def main():
    record_number = 0
    try:
        while True:
            record_number, status = stream_one_connection(record_number)
            if status >= 400:
                sys.exit(1)
    except KeyboardInterrupt:
        pass


main()
