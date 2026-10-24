"""共享无状态线级重定向围栏；准入、预算、账本仍由各阶段既有事务拥有。

One original wire request per admitted attempt. Never forward a bearer through
an unexpected redirect, including same-host or HTTPS-to-HTTP transitions.
"""
from urllib.error import HTTPError
from urllib.request import HTTPRedirectHandler, build_opener


class RedirectRefused(HTTPError):
    """保留原始3xx状态，不把重定向伪装为鉴权错误或完成响应。

    Preserve the observed status without exposing destination/header contents.
    """


class NoRedirect(HTTPRedirectHandler):
    def http_error_302(self, request, fp, code, message, headers):
        # Fence before urllib parses even a malformed/untrusted Location value.
        raise RedirectRefused(request.full_url, code, 'provider redirect refused', None, fp)

    http_error_301 = http_error_303 = http_error_307 = http_error_308 = http_error_302

    def redirect_request(self, request, fp, code, message, headers, newurl):
        raise RedirectRefused(request.full_url, code, 'provider redirect refused', None, fp)


def is_redirect_status(status):
    return type(status) is int and 300 <= status < 400


def urlopen(request, timeout):
    """使用独立 opener，不安装或修改跨线程全局 opener。

    Payload/headers/model defaults are untouched. HTTPError still carries the
    original provider status/body for the owning durable transaction to receipt.
    """
    try:
        response = build_opener(NoRedirect()).open(request, timeout=timeout)
    except HTTPError as error:
        if is_redirect_status(error.code) and not isinstance(error, RedirectRefused):
            raise RedirectRefused(request.full_url, error.code, 'provider redirect refused', None, error.fp) from None
        raise
    if is_redirect_status(getattr(response, 'status', None)):
        raise RedirectRefused(request.full_url, response.status, 'provider redirect refused', None, response)
    return response
