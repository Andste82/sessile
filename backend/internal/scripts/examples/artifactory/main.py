"""Artifactory for sessile tasks (PROJECT_PLAN.md §4.15.6).

Called as `main.py <function>` with the input as JSON on stdin; prints the
result as JSON on stdout. Settings arrive as environment variables. Plain
REST over `requests`, with the token as a bearer token.
"""
import json
import os
import sys
from urllib.parse import quote

import requests

URL = os.environ["ARTIFACTORY_URL"].rstrip("/")
DEFAULT_REPO = os.environ.get("ARTIFACTORY_REPO", "")
session = requests.Session()
session.headers["Authorization"] = "Bearer " + os.environ["ARTIFACTORY_TOKEN"]


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)


def call(path, **kwargs):
    try:
        r = session.get(URL + path, timeout=30, **kwargs)
    except requests.RequestException as e:
        fail("could not reach Artifactory: %s" % e.__class__.__name__)
    if r.status_code in (401, 403):
        fail("Artifactory refused the token (HTTP %d)" % r.status_code)
    if r.status_code == 404:
        fail("not found in Artifactory (HTTP 404)")
    if r.status_code >= 400:
        fail("Artifactory answered HTTP %d: %s" % (r.status_code, r.text[:500]))
    return r.json()


def clean_path(path):
    parts = [p for p in (path or "").strip("/").split("/") if p]
    if any(p in (".", "..") for p in parts):
        fail("a path can't contain . or ..")
    return "/".join(parts)


def repo_of(args):
    repo = args.get("repo") or DEFAULT_REPO
    if not repo:
        fail("no repository given and no default repository configured")
    return repo


def ping(_):
    repos = call("/api/repositories")
    return {"ok": True, "repositories": [r.get("key") for r in repos][:50]}


def search(args):
    params = {"name": args["name"]}
    repo = args.get("repo") or DEFAULT_REPO
    if repo:
        params["repos"] = repo
    res = call("/api/search/artifact", params=params)
    out = []
    for item in res.get("results", [])[:100]:
        # uri is .../api/storage/<repo>/<path>
        uri = item.get("uri", "")
        tail = uri.split("/api/storage/", 1)[-1]
        out.append({"repo": tail.split("/", 1)[0], "path": tail.split("/", 1)[-1]})
    return {"results": out}


def artifact_info(args):
    repo = repo_of(args)
    info = call("/api/storage/%s/%s" % (quote(repo), quote(clean_path(args["path"]))))
    return {
        "repo": repo,
        "path": info.get("path"),
        "size": info.get("size"),
        "created": info.get("created"),
        "lastModified": info.get("lastModified"),
        "checksums": info.get("checksums"),
        "downloadUri": info.get("downloadUri"),
    }


def list_path(args):
    repo = repo_of(args)
    path = clean_path(args.get("path"))
    info = call("/api/storage/%s/%s" % (quote(repo), quote(path)))
    return {
        "repo": repo,
        "path": "/" + path,
        "children": [{"name": c.get("uri", "").lstrip("/"), "folder": c.get("folder")}
                     for c in info.get("children", [])],
    }


FUNCTIONS = {f.__name__: f for f in (ping, search, artifact_info, list_path)}

if __name__ == "__main__":
    fn = FUNCTIONS.get(sys.argv[1] if len(sys.argv) > 1 else "")
    if fn is None:
        fail("unknown function")
    print(json.dumps(fn(json.load(sys.stdin))))
