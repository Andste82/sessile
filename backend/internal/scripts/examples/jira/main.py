"""Jira for sessile tasks (PROJECT_PLAN.md §4.15.6).

Called as `main.py <function>` with the input as JSON on stdin; prints the
result as JSON on stdout. Settings arrive as environment variables. Plain
REST over `requests`, API version 2, which both Jira Cloud and Server/Data
Center serve.
"""
import json
import os
import sys

import requests

URL = os.environ["JIRA_URL"].rstrip("/")
KIND = os.environ.get("JIRA_AUTH", "cloud")
TOKEN = os.environ["JIRA_TOKEN"]
USER = os.environ.get("JIRA_USER", "")
PROJECT = os.environ.get("JIRA_PROJECT", "")

session = requests.Session()
session.headers["Accept"] = "application/json"
if KIND == "cloud":
    session.auth = (USER, TOKEN)  # e-mail + API token, basic auth
else:
    session.headers["Authorization"] = "Bearer " + TOKEN  # personal access token


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)


def call(method, path, **kwargs):
    try:
        r = session.request(method, URL + path, timeout=30, **kwargs)
    except requests.RequestException as e:
        fail("could not reach Jira: %s" % e.__class__.__name__)
    if r.status_code in (401, 403):
        fail("Jira refused the credentials (HTTP %d)" % r.status_code)
    if r.status_code == 404:
        fail("not found in Jira (HTTP 404)")
    if r.status_code >= 400:
        fail("Jira answered HTTP %d: %s" % (r.status_code, r.text[:500]))
    return r.json() if r.content else {}


def person(p):
    if not p:
        return None
    return p.get("displayName") or p.get("name") or p.get("emailAddress")


def whoami(_):
    me = call("GET", "/rest/api/2/myself")
    return {"user": person(me), "account": me.get("accountId") or me.get("name")}


def read_ticket(args):
    key = args["key"]
    fields = "summary,description,status,issuetype,priority,assignee,reporter,labels,components,fixVersions,issuelinks,subtasks,parent,created,updated,comment"
    issue = call("GET", "/rest/api/2/issue/%s" % key, params={"fields": fields})
    f = issue.get("fields", {})
    links = []
    for link in f.get("issuelinks") or []:
        other = link.get("outwardIssue") or link.get("inwardIssue") or {}
        kind = link.get("type", {})
        links.append({
            "relation": kind.get("outward") if "outwardIssue" in link else kind.get("inward"),
            "key": other.get("key"),
            "summary": (other.get("fields") or {}).get("summary"),
        })
    comments = (f.get("comment") or {}).get("comments") or []
    return {
        "key": issue.get("key"),
        "url": "%s/browse/%s" % (URL, issue.get("key")),
        "summary": f.get("summary"),
        "type": (f.get("issuetype") or {}).get("name"),
        "status": (f.get("status") or {}).get("name"),
        "priority": (f.get("priority") or {}).get("name"),
        "assignee": person(f.get("assignee")),
        "reporter": person(f.get("reporter")),
        "labels": f.get("labels") or [],
        "components": [c.get("name") for c in f.get("components") or []],
        "fixVersions": [v.get("name") for v in f.get("fixVersions") or []],
        "parent": (f.get("parent") or {}).get("key"),
        "subtasks": [s.get("key") for s in f.get("subtasks") or []],
        "links": links,
        "created": f.get("created"),
        "updated": f.get("updated"),
        "description": f.get("description"),
        "comments": [{"author": person(c.get("author")), "created": c.get("created"), "body": c.get("body")}
                     for c in comments[-10:]],
    }


def search(args):
    fields = "summary,status,issuetype,assignee"
    params = {"jql": args["jql"], "maxResults": args.get("max", 25), "fields": fields}
    # Jira Cloud replaced /search with /search/jql; Server and Data Center
    # still serve /search.
    path = "/rest/api/2/search/jql" if KIND == "cloud" else "/rest/api/2/search"
    res = call("GET", path, params=params)
    out = []
    for issue in res.get("issues", []):
        f = issue.get("fields", {})
        out.append({
            "key": issue.get("key"),
            "summary": f.get("summary"),
            "status": (f.get("status") or {}).get("name"),
            "type": (f.get("issuetype") or {}).get("name"),
            "assignee": person(f.get("assignee")),
        })
    return {"issues": out}


def add_comment(args):
    c = call("POST", "/rest/api/2/issue/%s/comment" % args["key"], json={"body": args["body"]})
    return {"id": c.get("id"), "key": args["key"]}


def create_ticket(args):
    project = args.get("project") or PROJECT
    if not project:
        fail("no project given and no default project configured")
    fields = {
        "project": {"key": project},
        "summary": args["summary"],
        "issuetype": {"name": args.get("issuetype") or "Task"},
    }
    if args.get("description"):
        fields["description"] = args["description"]
    issue = call("POST", "/rest/api/2/issue", json={"fields": fields})
    return {"key": issue.get("key"), "url": "%s/browse/%s" % (URL, issue.get("key"))}


FUNCTIONS = {f.__name__: f for f in (whoami, read_ticket, search, add_comment, create_ticket)}

if __name__ == "__main__":
    fn = FUNCTIONS.get(sys.argv[1] if len(sys.argv) > 1 else "")
    if fn is None:
        fail("unknown function")
    print(json.dumps(fn(json.load(sys.stdin))))
