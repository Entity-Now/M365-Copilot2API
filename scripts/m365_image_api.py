#!/usr/bin/env python3
"""Standard-library CLI for M365-Copilot2API image understanding and generation."""

from __future__ import annotations

import argparse
import base64
import ipaddress
import json
import mimetypes
import os
import socket
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

DEFAULT_BASE_URL = "http://127.0.0.1:4141"
DEFAULT_MODEL = "gpt-image-2"
RETRY_STATUSES = {429, 502, 503}
ALLOWED_IMAGE_TYPES = {"image/png", "image/jpeg", "image/webp", "image/gif"}
MAX_INPUT_BYTES = 20 * 1024 * 1024
MAX_RESPONSE_BYTES = 50 * 1024 * 1024
DIAGNOSTIC_HEADERS = {
    "retry-after",
    "x-m365-proxy-error",
    "x-m365-ratelimit-remaining",
    "x-m365-retry-after",
    "x-m365-ratelimit-reset",
    "x-m365-global-circuit",
    "x-request-id",
}


class CliError(Exception):
    def __init__(self, message: str, *, kind: str = "input_error", exit_code: int = 2,
                 status: int | None = None, diagnostics: dict[str, str] | None = None):
        super().__init__(message)
        self.message = message
        self.kind = kind
        self.exit_code = exit_code
        self.status = status
        self.diagnostics = diagnostics or {}


def emit(value: Any, pretty: bool = False, stream=None) -> None:
    stream = stream or sys.stdout
    json.dump(value, stream, ensure_ascii=False, indent=2 if pretty else None,
              separators=None if pretty else (",", ":"))
    stream.write("\n")


def validate_base_url(value: str) -> str:
    parsed = urllib.parse.urlsplit(value.rstrip("/"))
    if parsed.scheme not in {"http", "https"} or not parsed.hostname:
        raise CliError("Base URL must be an absolute HTTP or HTTPS URL", kind="configuration_error")
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise CliError("Base URL must not contain credentials, query, or fragment", kind="configuration_error")
    if parsed.scheme == "http":
        try:
            address = ipaddress.ip_address(parsed.hostname)
            loopback = address.is_loopback
        except ValueError:
            loopback = parsed.hostname.lower() == "localhost"
        if not loopback:
            raise CliError("Plain HTTP is allowed only for loopback addresses", kind="configuration_error")
    return value.rstrip("/")


def config(args: argparse.Namespace, require_key: bool = True) -> tuple[str, str, str]:
    base_url = validate_base_url(args.base_url or os.getenv("M365_BASE_URL", DEFAULT_BASE_URL))
    api_key = args.api_key or os.getenv("M365_API_KEY", "")
    model = args.model or os.getenv("M365_MODEL", DEFAULT_MODEL)
    if require_key and not api_key:
        raise CliError("M365_API_KEY is not configured", kind="configuration_error", exit_code=3)
    if not model.strip():
        raise CliError("Model must not be empty", kind="configuration_error", exit_code=3)
    return base_url, api_key, model


def public_https_url(value: str) -> str:
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password:
        raise CliError("Remote images must use an HTTPS URL without embedded credentials")
    host = parsed.hostname.lower()
    if host == "localhost":
        raise CliError("Remote image URL must be public")
    try:
        addresses = {item[4][0] for item in socket.getaddrinfo(host, parsed.port or 443)}
    except socket.gaierror as exc:
        raise CliError(f"Could not resolve remote image host: {exc}") from exc
    for address in addresses:
        ip = ipaddress.ip_address(address)
        if not ip.is_global:
            raise CliError("Remote image URL must resolve only to public addresses")
    return value


def is_loopback_host(host: str | None) -> bool:
    if not host:
        return False
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def effective_port(parsed: urllib.parse.SplitResult) -> int:
    if parsed.port is not None:
        return parsed.port
    return 443 if parsed.scheme == "https" else 80 if parsed.scheme == "http" else 0


def is_gateway_url(url: str, base_url: str) -> bool:
    parsed_url = urllib.parse.urlsplit(url)
    parsed_base = urllib.parse.urlsplit(base_url)
    if parsed_url.username or parsed_url.password:
        return False
    if parsed_url.scheme != parsed_base.scheme:
        return False
    if effective_port(parsed_url) != effective_port(parsed_base):
        return False
    host_url = (parsed_url.hostname or "").lower()
    host_base = (parsed_base.hostname or "").lower()
    if host_url == host_base:
        return True
    if is_loopback_host(host_url) and is_loopback_host(host_base):
        return True
    return False


def resolve_generated_url(value: str, base_url: str) -> str:
    parsed = urllib.parse.urlsplit(value)
    if not parsed.scheme and (value.startswith("/") or not parsed.netloc):
        return urllib.parse.urljoin(base_url.rstrip("/") + "/", value.lstrip("/"))
    if is_gateway_url(value, base_url):
        return value
    return public_https_url(value)


def local_data_url(value: str) -> str:
    path = Path(value).expanduser().resolve()
    if not path.is_file() or path.is_symlink():
        raise CliError(f"Image is not a regular file: {path}")
    size = path.stat().st_size
    if size <= 0 or size > MAX_INPUT_BYTES:
        raise CliError(f"Image size must be between 1 and {MAX_INPUT_BYTES} bytes")
    mime = mimetypes.guess_type(path.name)[0]
    if mime not in ALLOWED_IMAGE_TYPES:
        raise CliError(f"Unsupported image type: {mime or 'unknown'}")
    encoded = base64.b64encode(path.read_bytes()).decode("ascii")
    return f"data:{mime};base64,{encoded}"


def image_reference(value: str) -> str:
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme.lower() in {"http", "https"}:
        return public_https_url(value)
    return local_data_url(value)


def safe_headers(headers) -> dict[str, str]:
    return {key.lower(): value for key, value in headers.items()
            if key.lower() in DIAGNOSTIC_HEADERS}


def retry_delay(headers, attempt: int) -> float:
    raw = headers.get("Retry-After") or headers.get("X-M365-Retry-After")
    if raw:
        try:
            return min(max(float(raw), 0.0), 30.0)
        except ValueError:
            pass
    return min(2 ** attempt, 8)


def request_json(base_url: str, api_key: str, endpoint: str, payload: dict[str, Any],
                 retries: int, timeout: float) -> tuple[dict[str, Any], dict[str, str]]:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(
        base_url + endpoint,
        data=body,
        method="POST",
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
    )
    parsed_base = urllib.parse.urlsplit(base_url)
    try:
        base_address = ipaddress.ip_address(parsed_base.hostname or "")
        is_loopback = base_address.is_loopback
    except ValueError:
        is_loopback = (parsed_base.hostname or "").lower() == "localhost"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({})) if is_loopback else urllib.request.build_opener()

    for attempt in range(retries + 1):
        try:
            with opener.open(request, timeout=timeout) as response:
                raw = response.read(MAX_RESPONSE_BYTES + 1)
                if len(raw) > MAX_RESPONSE_BYTES:
                    raise CliError("Gateway response exceeded the size limit", kind="response_error", exit_code=8)
                try:
                    value = json.loads(raw)
                except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                    raise CliError("Gateway returned invalid JSON", kind="response_error", exit_code=8) from exc
                if not isinstance(value, dict):
                    raise CliError("Gateway response must be a JSON object", kind="response_error", exit_code=8)
                return value, safe_headers(response.headers)
        except urllib.error.HTTPError as exc:
            raw = exc.read(MAX_RESPONSE_BYTES + 1)
            diagnostics = safe_headers(exc.headers)
            message = f"Gateway returned HTTP {exc.code}"
            error_type = "http_error"
            try:
                parsed = json.loads(raw)
                error = parsed.get("error", {}) if isinstance(parsed, dict) else {}
                if isinstance(error, dict):
                    message = str(error.get("message") or message)
                    error_type = str(error.get("type") or error.get("code") or error_type)
            except (UnicodeDecodeError, json.JSONDecodeError):
                pass
            if exc.code in RETRY_STATUSES and attempt < retries:
                time.sleep(retry_delay(exc.headers, attempt))
                continue
            exit_code = 4 if exc.code in {401, 403} else 5 if exc.code == 429 else 6 if exc.code in {502, 503} else 7
            raise CliError(message, kind=error_type, exit_code=exit_code,
                           status=exc.code, diagnostics=diagnostics) from exc
        except urllib.error.URLError as exc:
            if attempt < retries:
                time.sleep(min(2 ** attempt, 8))
                continue
            raise CliError(f"Transport error: {exc.reason}", kind="transport_error", exit_code=7) from exc
    raise AssertionError("unreachable")


def read_text(direct: str | None, filename: str | None, label: str) -> str:
    if bool(direct) == bool(filename):
        raise CliError(f"Specify exactly one of --{label} or --{label}-file")
    text = direct if direct is not None else Path(filename).read_text(encoding="utf-8")
    if not text.strip():
        raise CliError(f"{label.capitalize()} must not be empty")
    return text


def load_extra(args: argparse.Namespace) -> dict[str, Any]:
    if args.extra_json and args.extra_json_file:
        raise CliError("Use only one of --extra-json and --extra-json-file")
    raw = args.extra_json
    if args.extra_json_file:
        raw = Path(args.extra_json_file).read_text(encoding="utf-8")
    if not raw:
        return {}
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise CliError(f"Invalid extra JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise CliError("Extra JSON must be an object")
    forbidden = {"model", "prompt"}.intersection(value)
    if forbidden:
        raise CliError("Extra JSON cannot override model or prompt")
    return value


def unique_path(directory: Path, stem: str, suffix: str, overwrite: bool) -> Path:
    candidate = directory / f"{stem}{suffix}"
    if overwrite or not candidate.exists():
        return candidate
    index = 2
    while (directory / f"{stem}-{index}{suffix}").exists():
        index += 1
    return directory / f"{stem}-{index}{suffix}"


def atomic_write(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".m365-image-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def download_https(url: str, timeout: float) -> tuple[bytes, str]:
    public_https_url(url)
    request = urllib.request.Request(url, headers={"User-Agent": "m365-image-api/1"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        final_url = response.geturl()
        public_https_url(final_url)
        data = response.read(MAX_RESPONSE_BYTES + 1)
        if len(data) > MAX_RESPONSE_BYTES:
            raise CliError("Generated image exceeded the size limit", kind="file_output_error", exit_code=9)
        return data, response.headers.get_content_type()


def download_image(url: str, base_url: str, timeout: float, api_key: str = "") -> tuple[bytes, str]:
    target_url = resolve_generated_url(url, base_url)
    parsed = urllib.parse.urlsplit(target_url)
    is_loopback = is_loopback_host(parsed.hostname)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({})) if is_loopback else urllib.request.build_opener()
    headers = {"User-Agent": "m365-image-api/1"}
    if is_gateway_url(target_url, base_url) and api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    request = urllib.request.Request(target_url, headers=headers)
    with opener.open(request, timeout=timeout) as response:
        final_url = response.geturl()
        if not is_gateway_url(final_url, base_url):
            public_https_url(final_url)
        data = response.read(MAX_RESPONSE_BYTES + 1)
        if len(data) > MAX_RESPONSE_BYTES:
            raise CliError("Generated image exceeded the size limit", kind="file_output_error", exit_code=9)
        return data, response.headers.get_content_type()


def run_understand(args: argparse.Namespace) -> None:
    base_url, api_key, model = config(args)
    prompt = read_text(args.prompt, args.prompt_file, "prompt")
    content: list[dict[str, Any]] = [{"type": "text", "text": prompt}]
    content.extend({"type": "image_url", "image_url": {"url": image_reference(image)}}
                   for image in args.image)
    messages = []
    if args.system:
        messages.append({"role": "system", "content": args.system})
    messages.append({"role": "user", "content": content})
    response, diagnostics = request_json(base_url, api_key, "/v1/chat/completions",
                                         {"model": model, "messages": messages, "stream": False},
                                         args.retries, args.timeout)
    if args.raw:
        result = response
    else:
        try:
            choice = response["choices"][0]
            assistant = choice["message"]["content"]
        except (KeyError, IndexError, TypeError) as exc:
            raise CliError("Chat response did not contain assistant content", kind="response_error", exit_code=8) from exc
        result = {"content": assistant, "model": response.get("model", model),
                  "usage": response.get("usage"), "diagnostics": diagnostics or None}
    if args.output:
        Path(args.output).write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    if args.response_format == "text" and not args.raw:
        print(result["content"])
    else:
        emit(result, args.pretty)


def run_generate(args: argparse.Namespace) -> None:
    base_url, api_key, model = config(args)
    prompt = read_text(args.prompt, args.prompt_file, "prompt")
    payload = {"model": model, "prompt": prompt, **load_extra(args)}
    response, diagnostics = request_json(base_url, api_key, "/v1/images/generations",
                                         payload, args.retries, args.timeout)
    if args.raw:
        emit(response, args.pretty)
        return
    data = response.get("data")
    if not isinstance(data, list) or not data:
        raise CliError("Generation response contained no images", kind="response_error", exit_code=8)
    output_dir = Path(args.output_dir).expanduser().resolve()
    manifest = []
    for index, item in enumerate(data, 1):
        if not isinstance(item, dict):
            raise CliError("Generation item must be an object", kind="response_error", exit_code=8)
        entry: dict[str, Any] = {"index": index, "revised_prompt": item.get("revised_prompt")}
        if "b64_json" in item:
            entry["source_type"] = "b64_json"
            if args.no_download:
                entry["available"] = True
            else:
                try:
                    binary = base64.b64decode(item["b64_json"], validate=True)
                except (ValueError, TypeError) as exc:
                    raise CliError("Invalid b64_json image", kind="response_error", exit_code=8) from exc
                path = unique_path(output_dir, f"generated-{index}", ".png", args.overwrite)
                atomic_write(path, binary)
                entry.update(path=str(path), bytes=len(binary), media_type="image/png")
        elif "url" in item:
            url = resolve_generated_url(str(item["url"]), base_url)
            entry.update(source_type="url", url=url)
            if not args.no_download:
                binary, media_type = download_image(url, base_url, args.timeout, api_key)
                suffix = mimetypes.guess_extension(media_type) or (".png" if binary.startswith(b"\x89PNG") else ".img")
                path = unique_path(output_dir, f"generated-{index}", suffix, args.overwrite)
                atomic_write(path, binary)
                entry.update(path=str(path), bytes=len(binary), media_type=media_type)
        else:
            raise CliError("Generation item contained neither url nor b64_json", kind="response_error", exit_code=8)
        manifest.append(entry)
    emit({"images": manifest, "model": response.get("model", model),
          "created": response.get("created"), "diagnostics": diagnostics or None}, args.pretty)


def run_check(args: argparse.Namespace) -> None:
    base_url, api_key, model = config(args, require_key=False)
    emit({"ok": bool(api_key), "base_url": base_url, "model": model,
          "api_key_configured": bool(api_key)}, args.pretty)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(prog="m365_image_api.py", description=__doc__)
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--base-url")
    common.add_argument("--api-key", help=argparse.SUPPRESS)
    common.add_argument("--model")
    common.add_argument("--timeout", type=float, default=60.0)
    common.add_argument("--retries", type=int, default=2)
    common.add_argument("--pretty", action="store_true")
    commands = result.add_subparsers(dest="command", required=True)

    understand = commands.add_parser("understand", parents=[common], help="Understand one or more images")
    understand.add_argument("--image", action="append", required=True)
    understand.add_argument("--prompt")
    understand.add_argument("--prompt-file")
    understand.add_argument("--system")
    understand.add_argument("--response-format", choices=("json", "text"), default="json")
    understand.add_argument("--output")
    understand.add_argument("--raw", action="store_true")
    understand.set_defaults(func=run_understand)

    generate = commands.add_parser("generate", parents=[common], help="Generate images")
    generate.add_argument("--prompt")
    generate.add_argument("--prompt-file")
    generate.add_argument("--extra-json")
    generate.add_argument("--extra-json-file")
    generate.add_argument("--output-dir", default=".")
    generate.add_argument("--no-download", action="store_true")
    generate.add_argument("--raw", action="store_true")
    generate.add_argument("--overwrite", action="store_true")
    generate.set_defaults(func=run_generate)

    check = commands.add_parser("check", parents=[common], help="Validate local configuration")
    check.set_defaults(func=run_check)
    return result


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    try:
        if args.retries < 0 or args.timeout <= 0:
            raise CliError("Retries must be non-negative and timeout must be positive")
        args.func(args)
        return 0
    except CliError as exc:
        emit({"error": {"message": exc.message, "type": exc.kind,
                        "status": exc.status, "diagnostics": exc.diagnostics or None}}, stream=sys.stderr)
        return exc.exit_code
    except (OSError, ValueError) as exc:
        emit({"error": {"message": str(exc), "type": "file_output_error"}}, stream=sys.stderr)
        return 9


if __name__ == "__main__":
    raise SystemExit(main())
