"""
Minimal HS256 JWT utilities — no external dependencies.
Used by workspace_connect (generate) and ws_connect (verify).
"""
import base64
import hashlib
import hmac
import json
import time


def _b64url_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def _b64url_decode(s: str) -> bytes:
    pad = 4 - len(s) % 4
    return base64.urlsafe_b64decode(s + "=" * (pad % 4))


def encode(payload: dict, secret: str) -> str:
    """Sign a payload dict and return a compact JWT string."""
    header = _b64url_encode(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    body   = _b64url_encode(json.dumps(payload).encode())
    msg    = f"{header}.{body}".encode()
    sig    = hmac.new(secret.encode(), msg, hashlib.sha256).digest()
    return f"{header}.{body}.{_b64url_encode(sig)}"


def decode(token: str, secret: str) -> dict:
    """Verify signature + expiry and return claims dict."""
    parts = token.split(".")
    if len(parts) != 3:
        raise ValueError("malformed token")
    header, body, sig = parts
    msg      = f"{header}.{body}".encode()
    expected = hmac.new(secret.encode(), msg, hashlib.sha256).digest()
    if not hmac.compare_digest(_b64url_encode(expected), sig):
        raise ValueError("invalid signature")
    claims = json.loads(_b64url_decode(body))
    if claims.get("exp", 0) < time.time():
        raise ValueError("token expired")
    return claims
