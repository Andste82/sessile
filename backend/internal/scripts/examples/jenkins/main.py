"""Jenkins for sessile tasks (PROJECT_PLAN.md §4.15.6).

Called as `main.py <function>` with the input as JSON on stdin; prints the
result as JSON on stdout. Settings arrive as environment variables. Plain
REST over `requests`; an API token needs no CSRF crumb.
"""
import json
import os
import sys
from urllib.parse import quote

import requests

URL = os.environ["JENKINS_URL"].rstrip("/")
session = requests.Session()
session.auth = (os.environ["JENKINS_USER"], os.environ["JENKINS_TOKEN"])


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)


def call(method, path, want_json=True, **kwargs):
    try:
        r = session.request(method, URL + path, timeout=30, **kwargs)
    except requests.RequestException as e:
        fail("could not reach Jenkins: %s" % e.__class__.__name__)
    if r.status_code in (401, 403):
        fail("Jenkins refused the credentials (HTTP %d)" % r.status_code)
    if r.status_code == 404:
        fail("not found in Jenkins (HTTP 404) — check the job path")
    if r.status_code >= 400:
        fail("Jenkins answered HTTP %d: %s" % (r.status_code, r.text[:500]))
    return r.json() if want_json else r


def job_path(job):
    # folder/sub/job -> /job/folder/job/sub/job/job
    parts = [p for p in job.strip("/").split("/") if p]
    if any(p in (".", "..") for p in parts):
        fail("a job path can't contain . or ..")
    return "".join("/job/" + quote(p, safe="") for p in parts)


def whoami(_):
    me = call("GET", "/me/api/json")
    return {"user": me.get("fullName") or me.get("id")}


def job_status(args):
    tree = "name,url,color,lastBuild[number,result,building,timestamp,duration,url]"
    job = call("GET", job_path(args["job"]) + "/api/json", params={"tree": tree})
    last = job.get("lastBuild") or {}
    return {
        "job": args["job"],
        "url": job.get("url"),
        "lastBuild": {
            "number": last.get("number"),
            "result": "RUNNING" if last.get("building") else last.get("result"),
            "startedAtMs": last.get("timestamp"),
            "durationMs": last.get("duration"),
            "url": last.get("url"),
        } if last else None,
    }


def build_log_tail(args):
    build = str(args.get("build") or "lastBuild")
    lines = args.get("lines", 200)
    r = call("GET", job_path(args["job"]) + "/" + build + "/consoleText", want_json=False)
    text = r.text.splitlines()
    return {"job": args["job"], "build": build, "lines": text[-lines:], "totalLines": len(text)}


def trigger_build(args):
    params = args.get("parameters") or {}
    endpoint = "/buildWithParameters" if params else "/build"
    r = call("POST", job_path(args["job"]) + endpoint, want_json=False,
             params={k: str(v) for k, v in params.items()})
    return {"job": args["job"], "queued": True, "queueUrl": r.headers.get("Location")}


FUNCTIONS = {f.__name__: f for f in (whoami, job_status, build_log_tail, trigger_build)}

if __name__ == "__main__":
    fn = FUNCTIONS.get(sys.argv[1] if len(sys.argv) > 1 else "")
    if fn is None:
        fail("unknown function")
    print(json.dumps(fn(json.load(sys.stdin))))
