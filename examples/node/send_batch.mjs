import { gzipSync } from "node:zlib";

const baseUrl = process.env.SCRAPELOG_URL ?? "http://127.0.0.1:8080";
const apiKey = process.env.SCRAPELOG_KEY;
const recordCount = 1000;

if (!apiKey) {
  console.error("SCRAPELOG_KEY is required");
  process.exit(1);
}

const records = Array.from({ length: recordCount }, (_, pageNumber) => ({
  method: "GET",
  url: `https://example.com/page/${pageNumber}`,
  status: 200,
  latency_ms: 120 + (pageNumber % 50),
  scraper: "example-node",
  request_headers: {
    "User-Agent": "example-scraper/1.0",
    Authorization: "Bearer example-token-that-will-be-redacted",
  },
}));

const response = await fetch(`${baseUrl}/v1/requests`, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${apiKey}`,
    "Content-Type": "application/json",
    "Content-Encoding": "gzip",
  },
  body: gzipSync(JSON.stringify(records)),
});

const responseText = await response.text();
if (!response.ok) {
  console.error(responseText);
  process.exit(1);
}
console.log(responseText);
