#!/usr/bin/env python3
"""fetch: Retrieve raw webpage content via HTTP GET.

Protocol v2 plugin. Fetches a URL directly (no external API) and returns
the content as HTML, stripped plain text, or markdown.

Config keys:
  user_agent          - Custom UA string (default: ductile-fetch/1.0)
  timeout_seconds     - Request timeout in seconds (default: 30)
  follow_redirects    - Follow HTTP redirects (default: false; see manifest)
  output_format       - html | text | markdown (default: html)
                        markdown: sends Accept: text/markdown for content
                        negotiation; falls back to HTML if the server does
                        not honour it.
  max_response_bytes  - Hard cap on response body, in bytes (default: 1048576)
  allowed_hosts       - Optional list. When non-empty, only requests whose
                        hostname matches an entry pass (exact match). Hosts
                        in this list bypass the IP blocklist (operator opt-in
                        for trusted internal / loopback targets).
  denied_hosts        - Optional list. Requests whose hostname matches an
                        entry are rejected, regardless of IP. Deny is
                        checked before allow.

See `_validate_target` for the full egress policy and `_pinned_getaddrinfo`
for the DNS-rebinding-resistant connect path.
"""

import html.parser
import ipaddress
import json
import socket
import sys
import urllib.error
import urllib.request
from urllib.parse import urlparse

# ---------------------------------------------------------------------------
# Read request
# ---------------------------------------------------------------------------

request = json.loads(sys.stdin.read())
command = request.get("command", "handle")
config = request.get("config", {})
event = request.get("event", {})

USER_AGENT = config.get("user_agent", "ductile-fetch/1.0")
TIMEOUT = int(config.get("timeout_seconds", 30))
FOLLOW_REDIRECTS = str(config.get("follow_redirects", "false")).lower() == "true"
OUTPUT_FORMAT = config.get("output_format", "html").lower()
MAX_RESPONSE_BYTES = int(config.get("max_response_bytes", 1024 * 1024))
ALLOWED_HOSTS = {h.strip().lower() for h in config.get("allowed_hosts", []) if h and h.strip()}
DENIED_HOSTS = {h.strip().lower() for h in config.get("denied_hosts", []) if h and h.strip()}

# AWS / GCP / Azure-style metadata host. Belongs to the link-local block but
# called out for clarity in error messages.
_METADATA_HOSTS = {"169.254.169.254", "fd00:ec2::254"}

# ---------------------------------------------------------------------------
# DNS-rebinding-resistant resolution (P2-06)
# ---------------------------------------------------------------------------
#
# _validate_target() resolves a hostname and rejects internal IPs. Without
# pinning, urllib would re-resolve at connect time — a short-TTL DNS server
# can return a public IP for the validation lookup and a private IP for the
# connect lookup, defeating the blocklist (DNS rebinding TOCTOU).
#
# We install a socket.getaddrinfo wrapper at module import. _validate_target
# populates _DNS_PIN with the validated IPs, and the wrapper returns those
# same IPs at connect time so urllib's TCP socket lands on the address we
# audited — not whatever DNS returns on the second lookup. The plugin is
# fork-per-invocation so global state is request-scoped in practice.

_DNS_PIN: dict[str, list[str]] = {}
_ORIGINAL_GETADDRINFO = socket.getaddrinfo


def _pinned_getaddrinfo(host, port, *args, **kwargs):
    key = (host or "").lower()
    pinned = _DNS_PIN.get(key)
    if not pinned:
        return _ORIGINAL_GETADDRINFO(host, port, *args, **kwargs)

    results = []
    for raw in pinned:
        try:
            addr = ipaddress.ip_address(raw)
        except ValueError:
            continue
        if isinstance(addr, ipaddress.IPv4Address):
            results.append((socket.AF_INET, socket.SOCK_STREAM, 0, "", (str(addr), port)))
        else:
            results.append((socket.AF_INET6, socket.SOCK_STREAM, 0, "", (str(addr), port, 0, 0)))
    if not results:
        return _ORIGINAL_GETADDRINFO(host, port, *args, **kwargs)
    return results


socket.getaddrinfo = _pinned_getaddrinfo


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def respond(status, result=None, error=None, retry=True, events=None, logs=None):
    resp = {"status": status}
    if error:
        resp["error"] = error
        resp["retry"] = retry
    if result is not None:
        resp["result"] = result
    if events:
        resp["events"] = events
    resp["logs"] = logs or []
    json.dump(resp, sys.stdout)
    sys.exit(0)


class _TextExtractor(html.parser.HTMLParser):
    """Minimal tag-stripping HTML parser."""

    _SKIP = {"script", "style", "head", "noscript"}

    def __init__(self):
        super().__init__()
        self._skip_depth = 0
        self._parts = []

    def handle_starttag(self, tag, attrs):
        if tag in self._SKIP:
            self._skip_depth += 1

    def handle_endtag(self, tag):
        if tag in self._SKIP and self._skip_depth:
            self._skip_depth -= 1

    def handle_data(self, data):
        if not self._skip_depth:
            stripped = data.strip()
            if stripped:
                self._parts.append(stripped)

    def text(self):
        return "\n".join(self._parts)


def html_to_text(raw_html):
    parser = _TextExtractor()
    parser.feed(raw_html)
    return parser.text()


class EgressRejectedError(Exception):
    """Raised when a URL is rejected by the egress policy (P2-06)."""


def _validate_target(url):
    """Validate one URL against the egress policy.

    Returns the parsed hostname (lowercased) on success. Raises
    EgressRejectedError on policy violation. Resolves DNS to check every
    returned address — a hostile or compromised DNS that returns a private
    IP for a public name is rejected at this layer.

    The validated IPs are pinned into the module-level _DNS_PIN map so the
    subsequent connect-time getaddrinfo (via _pinned_getaddrinfo) sees the
    SAME addresses — no second DNS round-trip the attacker can poison
    (DNS rebinding TOCTOU defense).
    """
    parsed = urlparse(url)
    scheme = (parsed.scheme or "").lower()
    if scheme not in {"http", "https"}:
        raise EgressRejectedError(f"scheme {scheme!r} not in {{http, https}}: {url}")
    if not parsed.hostname:
        raise EgressRejectedError(f"missing hostname: {url}")

    host = parsed.hostname.lower()

    if host in DENIED_HOSTS:
        raise EgressRejectedError(f"host {host!r} is in denied_hosts")

    if ALLOWED_HOSTS:
        if host not in ALLOWED_HOSTS:
            raise EgressRejectedError(f"host {host!r} not in allowed_hosts")
        # Explicitly allowed host bypasses IP blocklist. Resolve and pin
        # anyway so the connect-time lookup uses the same addresses we
        # observed at admission — rebinding defense applies even when the
        # blocklist is bypassed.
        _pin_resolution(host)
        return host

    addresses = _resolve_and_validate_ips(host)
    _DNS_PIN[host] = list(addresses)
    return host


def _resolve_and_validate_ips(host):
    """Resolve `host` and reject every returned IP that fails the policy.

    Returns the resolved IP strings on success; raises EgressRejectedError on
    any policy failure. Separated from _validate_target so allow-list opt-in
    can pin without re-running the blocklist.
    """
    try:
        _, _, addresses = socket.gethostbyname_ex(host)
    except socket.gaierror as exc:
        raise EgressRejectedError(f"DNS resolution failed for {host!r}: {exc}") from exc
    if not addresses:
        raise EgressRejectedError(f"DNS returned no addresses for {host!r}")

    for raw_addr in addresses:
        try:
            addr = ipaddress.ip_address(raw_addr)
        except ValueError:
            raise EgressRejectedError(f"unparseable address {raw_addr!r} for {host!r}")

        # Normalize IPv4-mapped IPv6 (::ffff:127.0.0.1 etc.) to its IPv4 form
        # so the policy decisions below apply uniformly.
        if isinstance(addr, ipaddress.IPv6Address) and addr.ipv4_mapped is not None:
            addr = addr.ipv4_mapped

        if str(addr) in _METADATA_HOSTS:
            raise EgressRejectedError(f"address {addr} is a cloud metadata endpoint")
        if addr.is_loopback:
            raise EgressRejectedError(f"address {addr} is loopback")
        if addr.is_link_local:
            raise EgressRejectedError(f"address {addr} is link-local")
        if addr.is_private:
            raise EgressRejectedError(f"address {addr} is private (RFC1918 / ULA)")
        if addr.is_multicast:
            raise EgressRejectedError(f"address {addr} is multicast")
        if addr.is_unspecified:
            raise EgressRejectedError(f"address {addr} is unspecified (0.0.0.0 / ::)")
        if addr.is_reserved:
            raise EgressRejectedError(f"address {addr} is reserved")

    return addresses


def _pin_resolution(host):
    """Resolve `host` with the ORIGINAL getaddrinfo and pin the result.

    Used on the allow-list bypass path: the operator has opted into the host
    but we still want the connect to land on the addresses we observed at
    admission, not on whatever a rebinding-capable DNS server returns later.
    """
    try:
        _, _, addresses = socket.gethostbyname_ex(host)
    except socket.gaierror:
        # If even resolution fails, leave _DNS_PIN unset and let urllib's
        # own resolver surface the error at connect time.
        return
    if addresses:
        _DNS_PIN[host] = list(addresses)


class _ValidatingRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Re-validate each redirect target against the egress policy.

    Default urllib follows redirects without revalidation; a public URL can
    legitimately redirect to a private/loopback host, defeating any
    initial-URL check. This handler intercepts each hop.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        _validate_target(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _build_opener():
    if FOLLOW_REDIRECTS:
        return urllib.request.build_opener(_ValidatingRedirectHandler())
    # Suppress automatic redirect following: 3xx responses surface as HTTPError.
    return urllib.request.build_opener(urllib.request.HTTPErrorProcessor())


def fetch_url(url):
    """Return (body_str, status_code, final_url, markdown_tokens, server_sent_markdown).

    Raises EgressRejected if the initial URL or any redirect hop violates
    the egress policy.
    """
    _validate_target(url)

    headers = {"User-Agent": USER_AGENT}
    if OUTPUT_FORMAT == "markdown":
        headers["Accept"] = "text/markdown, text/html"

    req = urllib.request.Request(url, headers=headers)
    opener = _build_opener()

    with opener.open(req, timeout=TIMEOUT) as resp:
        # Read one byte past the cap so we can detect overflow without
        # buffering an unbounded body.
        raw = resp.read(MAX_RESPONSE_BYTES + 1)
        if len(raw) > MAX_RESPONSE_BYTES:
            raise EgressRejectedError(
                f"response body exceeds max_response_bytes ({MAX_RESPONSE_BYTES})"
            )
        status_code = resp.status
        final_url = resp.url
        content_type = resp.headers.get("Content-Type", "")
        markdown_tokens = resp.headers.get("x-markdown-tokens")
        charset = resp.headers.get_content_charset() or "utf-8"

    body = raw.decode(charset, errors="replace")
    server_sent_markdown = "text/markdown" in content_type

    return body, status_code, final_url, markdown_tokens, server_sent_markdown


# ---------------------------------------------------------------------------
# Command handlers
# ---------------------------------------------------------------------------

if command == "health":
    respond("ok", result="healthy", logs=[{"level": "info", "message": "healthy"}])

elif command == "handle":
    url = event.get("payload", {}).get("url") or event.get("url")
    if not url:
        respond(
            "error",
            error="event must include url",
            retry=False,
            logs=[{"level": "error", "message": "handle: no url in event payload"}],
        )

    try:
        body, status_code, final_url, markdown_tokens, server_sent_markdown = fetch_url(url)
    except EgressRejectedError as exc:
        # Egress policy violations are configuration / target issues, not
        # transient. Non-retryable.
        respond(
            "error",
            error=f"egress rejected: {exc}",
            retry=False,
            events=[{"type": "fetch.failed", "payload": {"url": url, "error": str(exc)}}],
            logs=[{"level": "error", "message": f"fetch egress rejected for {url}: {exc}"}],
        )
    except urllib.error.HTTPError as exc:
        respond(
            "error",
            error=f"HTTP {exc.code}: {exc.reason}",
            events=[{"type": "fetch.failed", "payload": {"url": url, "error": f"HTTP {exc.code}: {exc.reason}"}}],
            logs=[{"level": "error", "message": f"fetch failed for {url}: HTTP {exc.code}"}],
        )
    except (urllib.error.URLError, OSError, ValueError) as exc:
        respond(
            "error",
            error=str(exc),
            events=[{"type": "fetch.failed", "payload": {"url": url, "error": str(exc)}}],
            logs=[{"level": "error", "message": f"fetch failed for {url}: {exc}"}],
        )

    effective_format = OUTPUT_FORMAT
    if OUTPUT_FORMAT == "markdown" and not server_sent_markdown:
        # Server didn't honour Accept: text/markdown — fall back to HTML
        content = body
        effective_format = "html"
    elif OUTPUT_FORMAT == "text":
        content = html_to_text(body)
    else:
        content = body

    logs = [
        {
            "level": "info",
            "message": f"fetched {url} → {status_code} ({len(content)} chars, format={effective_format})",
        }
    ]
    if final_url != url:
        logs.append({"level": "info", "message": f"redirected to {final_url}"})
    if OUTPUT_FORMAT == "markdown" and not server_sent_markdown:
        logs.append({"level": "warn", "message": "server did not return text/markdown; fell back to html"})

    payload = {
        "url": url,
        "final_url": final_url,
        "status_code": status_code,
        "content_length": len(content),
        "output_format": effective_format,
        "content": content,
    }
    if markdown_tokens is not None:
        # x-markdown-tokens is remote-controlled; a hostile or malformed
        # value must produce a bounded protocol error, not an unhandled
        # crash with no response (C-FRO-10).
        try:
            payload["markdown_tokens"] = int(markdown_tokens)
        except (ValueError, TypeError):
            respond(
                "error",
                error=f"invalid x-markdown-tokens header from {final_url}: {markdown_tokens!r}",
                retry=False,
                events=[{"type": "fetch.failed", "payload": {"url": url, "error": "invalid x-markdown-tokens header"}}],
                logs=[{"level": "error", "message": f"invalid x-markdown-tokens header: {markdown_tokens!r}"}],
            )

    respond(
        "ok",
        result=f"fetched {url} ({len(content)} chars, format={effective_format})",
        events=[{"type": "fetch.completed", "payload": payload}],
        logs=logs,
    )

else:
    respond(
        "error",
        error=f"unknown command: {command}",
        retry=False,
        logs=[{"level": "error", "message": f"unknown command: {command}"}],
    )
