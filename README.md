# SSRF Go Tester

A local, single-request SSRF testing workbench for authorized Security QA.

## Ubuntu setup

Requires Go 1.22+.

```bash
cd ssrf-go-tester
go run .
```

Open http://127.0.0.1:8766 in your browser.

To build a binary:

```bash
go build -o ssrf-go-tester .
./ssrf-go-tester
```

Enter an HTTPS API endpoint and a test URL. Insert `{{TEST_URL}}` in a JSON body, or in the endpoint query string. Send one request and inspect the response. The app does not automatically confirm vulnerabilities. For remote out-of-band detection use an externally reachable callback service, such as Interactsh, and inspect callbacks there.

## Safety and limits

- Bound to 127.0.0.1; local browser only.
- Single HTTP request at a time per click; no scan automation.
- Only HTTPS targets; no redirects; rejects private and loopback DNS destinations.
- Request body limited to 64 KiB; response preview limited to 16 KiB.
- No request history stored on disk. Avoid pasting production secrets.
- The local callback receiver is not reachable from remote targets unless separately exposed; do not expose the entire UI to the Internet.
