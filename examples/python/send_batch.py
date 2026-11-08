import gzip
import json
import os
import sys
import urllib.error
import urllib.request

BASE_URL = os.environ.get("SCRAPELOG_URL", "http://127.0.0.1:8080")
API_KEY = os.environ["SCRAPELOG_KEY"]
RECORD_COUNT = 1000


def build_records():
    return [
        {
            "method": "GET",
            "url": f"https://example.com/page/{page_number}",
            "status": 200,
            "latency_ms": 120 + page_number % 50,
            "scraper": "example-python",
            "request_headers": {
                "User-Agent": "example-scraper/1.0",
                "Authorization": "Bearer example-token-that-will-be-redacted",
            },
        }
        for page_number in range(RECORD_COUNT)
    ]


def main():
    compressed_body = gzip.compress(json.dumps(build_records()).encode("utf-8"))
    request = urllib.request.Request(
        BASE_URL + "/v1/requests",
        data=compressed_body,
        method="POST",
        headers={
            "Authorization": "Bearer " + API_KEY,
            "Content-Type": "application/json",
            "Content-Encoding": "gzip",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            print(response.read().decode("utf-8"))
    except urllib.error.HTTPError as http_error:
        print(http_error.read().decode("utf-8"), file=sys.stderr)
        sys.exit(1)


main()
