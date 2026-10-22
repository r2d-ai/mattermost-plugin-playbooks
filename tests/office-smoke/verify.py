#!/usr/bin/env python3
# Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
# See LICENSE.txt for license information.

"""Exercise Office integration against the disposable, actual Mattermost 12 host.

The target is deliberately fixed to localhost:18065. See README.md to start the
isolated containers. The admin-only caller fixture must never be installed in a
shared Mattermost deployment.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


BASE_URL = "http://127.0.0.1:18065"
CONTAINER = "playbooks-office-smoke-mm"
ADMIN = "office-smoke-admin"
PASSWORD = "OfficeSmoke-Only123!"
API = "/plugins/playbooks/api/v0"


def check(condition, label):
    if not condition:
        raise AssertionError(label)
    print(f"PASS {label}", flush=True)


def request(method, path, body=None, token=None, expected=200, headers=None):
    request_headers = dict(headers or {})
    if token:
        request_headers["Authorization"] = f"Bearer {token}"
    if isinstance(body, bytes):
        data = body
    elif body is None:
        data = None
    else:
        data = json.dumps(body).encode()
        request_headers["Content-Type"] = "application/json"
    req = urllib.request.Request(BASE_URL + path, data=data, headers=request_headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=60)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        content = response.read()
        try:
            result = json.loads(content) if content else None
        except json.JSONDecodeError:
            result = content.decode(errors="replace")
        if response.status != expected:
            raise AssertionError(f"{method} {path}: expected {expected}, got {response.status}: {result}")
        return result, response.headers


def login(username):
    data, headers = request("POST", "/api/v4/users/login", {"login_id": username, "password": PASSWORD})
    return data, headers["Token"]


def wait_for_host():
    for _ in range(60):
        try:
            body, _ = request("GET", "/api/v4/system/ping")
            if body["status"] == "OK":
                return
        except (AssertionError, urllib.error.URLError, ConnectionError):
            pass
        time.sleep(1)
    raise AssertionError("Disposable Mattermost host did not start")


def fixture_bundle(binary, plugin_id):
    manifest = {
        "id": plugin_id,
        "name": "Disposable Office smoke caller",
        "description": "Admin-only test relay. Never deploy outside the isolated Office smoke host.",
        "version": "0.0.1",
        "min_server_version": "12.0.0",
        "server": {"executables": {"linux-amd64": "server/caller"}},
    }
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as bundle:
        data = json.dumps(manifest).encode()
        info = tarfile.TarInfo(f"{plugin_id}/plugin.json")
        info.size = len(data)
        info.mode = 0o644
        bundle.addfile(info, io.BytesIO(data))
        bundle.add(binary, arcname=f"{plugin_id}/server/caller")
    return output.getvalue()


def upload(data, token):
    boundary = "office-smoke-" + uuid.uuid4().hex
    body = (
        f'--{boundary}\r\nContent-Disposition: form-data; name="force"\r\n\r\ntrue\r\n'
        f'--{boundary}\r\nContent-Disposition: form-data; name="plugin"; filename="plugin.tar.gz"\r\n'
        "Content-Type: application/gzip\r\n\r\n"
    ).encode() + data + f"\r\n--{boundary}--\r\n".encode()
    request("POST", "/api/v4/plugins", body, token, 201,
            {"Content-Type": f"multipart/form-data; boundary={boundary}"})


def verify(bundle, caller_binary):
    image = subprocess.check_output(
        ["docker", "inspect", "--format", "{{.Config.Image}}", CONTAINER], text=True,
    ).strip()
    check(image == "mattermost/mattermost-team-edition:12.0.0", "host uses the actual Mattermost 12.0.0 Docker image")
    version = subprocess.check_output(["docker", "exec", CONTAINER, "/mattermost/bin/mattermost", "version"], text=True)
    check("Version: 12.0.0" in version, "host binary reports version 12.0.0")
    wait_for_host()
    admin, admin_token = login(ADMIN)
    config, _ = request("GET", "/api/v4/config", token=admin_token)
    check(config["PluginSettings"]["AutomaticPrepackagedPlugins"] is False,
          "automatic stock-plugin replacement is disabled for the custom bundle")
    bundle_data = bundle.read_bytes()
    print(f"Bundle SHA256 {hashlib.sha256(bundle_data).hexdigest()}", flush=True)
    upload(bundle_data, admin_token)
    request("POST", "/api/v4/plugins/playbooks/enable", token=admin_token)
    check(True, "Playbooks activates on Mattermost 12 without a license or host-version override")

    for plugin_id in ("company-portal", "office-smoke-untrusted"):
        upload(fixture_bundle(caller_binary, plugin_id), admin_token)
        request("POST", f"/api/v4/plugins/{plugin_id}/enable", token=admin_token)

    def relay(method, path, body=None, actor=None, expected=200, plugin_id="company-portal", user_id=None):
        payload = {"method": method, "path": "/api/v0/office" + path,
                   "actor_user_id": admin["id"] if actor is None else actor}
        if body is not None:
            payload["body"] = body
        if user_id is not None:
            payload["user_id"] = user_id
        return request("POST", f"/plugins/{plugin_id}/relay", payload, admin_token, expected)[0]

    capabilities = relay("GET", "/capabilities")
    check(capabilities["schema_version"] == 1 and "external-approval-gates" in capabilities["capabilities"],
          "real PluginHTTP caller receives capabilities")
    request("GET", API + "/office/capabilities", token=admin_token, expected=403,
            headers={"Mattermost-Plugin-ID": "company-portal", "Mattermost-Plugin-Acting-User-Id": admin["id"]})
    check(True, "host strips browser attempts to spoof the trusted plugin header")
    relay("GET", "/capabilities", actor="", expected=401)
    relay("GET", "/capabilities", actor="invalid", expected=401)
    relay("GET", "/capabilities", plugin_id="office-smoke-untrusted", expected=403)
    check(True, "missing actors, invalid actors, and untrusted plugin callers are denied")

    suffix = uuid.uuid4().hex[:8]
    team, _ = request("POST", "/api/v4/teams", {
        "name": "office-smoke-" + suffix, "display_name": "Office smoke", "type": "O",
    }, admin_token, 201)
    users = []
    for name in ("reviewer", "outsider"):
        username = f"office-{name}-{suffix}"
        user, _ = request("POST", "/api/v4/users", {
            "username": username, "email": f"{username}@example.com", "password": PASSWORD,
        }, admin_token, 201)
        request("POST", f'/api/v4/teams/{team["id"]}/members', {
            "team_id": team["id"], "user_id": user["id"],
        }, admin_token, 201)
        _, token = login(username)
        users.append((user, token))
    (reviewer, reviewer_token), (outsider, outsider_token) = users
    request("POST", "/plugins/company-portal/relay", {}, reviewer_token, 403)
    check(relay("GET", "/capabilities", user_id=reviewer["id"]) == capabilities,
          "fixture denies non-admin users and host neutralizes conflicting raw user headers")

    playbook, _ = request("POST", API + "/playbooks", {
        "title": "Office smoke " + suffix, "team_id": team["id"], "public": True,
        "create_public_playbook_run": False, "reminder_timer_default_seconds": 3600,
        "auto_archive_channel": True,
        "invite_users_enabled": True, "invited_user_ids": [admin["id"]],
        "channel_mode": "create_new_channel", "checklists": [{"title": "Work", "items": [
            {"title": "Dependent work", "state": "", "assignee_id": admin["id"]},
            {"title": "Other work", "state": "", "assignee_id": admin["id"]},
        ]}],
    }, admin_token, 201)
    playbook_path = API + "/playbooks/" + playbook["id"]
    template_field, _ = request("POST", playbook_path + "/property_fields", {
        "name": "Release condition", "type": "text", "attrs": {"visibility": "always", "sort_order": 0},
    }, admin_token, 201)
    template_condition, _ = request("POST", playbook_path + "/conditions", {
        "version": 1, "condition_expr": {"is": {"field_id": template_field["id"], "value": "yes"}},
    }, admin_token, 201)
    template, _ = request("GET", playbook_path, token=admin_token)
    template["checklists"][0]["items"].append({
        "title": "Hidden conditional work", "state": "", "assignee_id": admin["id"],
        "condition_id": template_condition["id"],
    })
    request("PUT", playbook_path, template, admin_token)
    creation = {
        "operation_key": "office:smoke:" + suffix, "request_id": "request-" + suffix,
        "purpose": "smoke", "generation": 1, "source_version": 1,
        "playbook_id": playbook["id"], "owner_user_id": admin["id"], "team_id": team["id"],
        "name": "Office smoke " + suffix, "summary": "Disposable integration verification",
    }
    created = relay("POST", "/runs", creation)
    check(created == relay("POST", "/runs", creation), "repeated provisioning returns the same run and channel")
    relay("POST", "/runs", {**creation, "summary": "conflicting payload"}, expected=409)
    check(True, "conflicting reuse of a provisioning key is rejected")
    by_key = "/runs/by-key?" + urllib.parse.urlencode({"operation_key": creation["operation_key"]})
    check(relay("GET", by_key) == created, "provisioning can be recovered by operation key")
    request("PUT", f'/api/v4/channels/{created["channel_id"]}/patch', {
        "name": "office-smoke-renamed-" + suffix, "header": "Edited after Office provisioning",
    }, admin_token)
    check(relay("POST", "/runs", creation) == created,
          "provisioning replay keeps its bound channel after the channel name and header change")
    run_path = API + "/runs/" + created["run_id"]
    run, _ = request("GET", run_path, token=admin_token)
    check(len(run["property_fields"]) == 1 and run["property_fields"][0]["id"] != template_field["id"],
          "Office provisioning creates a real run property with a host-allocated ID")
    run_field = run["property_fields"][0]
    run_conditions, _ = request("GET", run_path + "/conditions", token=admin_token)
    check(len(run_conditions["items"]) == 1 and
          run_conditions["items"][0]["condition_expr"]["is"]["field_id"] == run_field["id"],
          "copied condition references the actual run property ID")
    conditional_task = run["checklists"][0]["items"][2]
    check(conditional_task["condition_id"] == run_conditions["items"][0]["id"] and
          conditional_task["condition_action"] == "hidden", "conditional work is initially hidden when its property is unset")
    request("PUT", playbook_path + "/property_fields/" + template_field["id"], {
        "name": "Changed after provisioning", "type": "text", "attrs": {"visibility": "always", "sort_order": 0},
    }, admin_token)
    task_id = run["checklists"][0]["items"][0]["id"]
    request("POST", f'/api/v4/channels/{created["channel_id"]}/members', {"user_id": reviewer["id"]}, admin_token, 201)

    page = relay("GET", "/tasks?limit=1")
    check(len(page["items"]) == 1 and page["next_cursor"], "task inbox pages active tasks assigned to the actor")
    next_page = relay("GET", "/tasks?" + urllib.parse.urlencode({"limit": 1, "cursor": page["next_cursor"]}))
    check(len(next_page["items"]) == 1 and next_page["items"][0]["id"] != page["items"][0]["id"],
          "task cursor advances without repeating a task")
    inbox = relay("GET", "/tasks")
    check(relay("GET", "/tasks", user_id=outsider["id"]) == inbox,
          "a raw user header cannot override the actor used for the Office inbox")
    check(all(item["id"] != conditional_task["id"] for item in inbox["items"]), "hidden conditional work is excluded from the Office inbox")
    check(relay("GET", "/tasks", actor=outsider["id"])["items"] == [], "outsider receives no tasks")
    relay("GET", "/tasks?user_id=" + outsider["id"], expected=400)

    projection = {
        "source_key": "approval:" + suffix, "source_version": 1,
        "request_id": "request-" + suffix, "approval_id": "approval-" + suffix,
        "title": "Budget approval", "requester_user_id": admin["id"],
        "assignee_user_id": admin["id"], "guards": ["NotRequester"], "status": "pending",
        "dependent_task_ids": [task_id],
    }
    gates_path = "/runs/" + created["run_id"] + "/approval-gates"
    first_gate = relay("POST", gates_path, projection)
    check(first_gate == relay("POST", gates_path, projection), "repeating a gate projection is idempotent")
    views, _ = request("GET", run_path + "/approval-gates", token=admin_token)
    check(len(views) == 1 and not views[0]["can_review"], "separation-of-duties guard prevents self review")
    check(set(views[0]) == {"gate_id", "title", "status", "path", "can_review"}, "browser gate view exposes no actor IDs or guards")
    check(views[0]["path"] == "/office?page=%2Fwork%2Fapprovals%2Fapproval-" + suffix, "Office deep link is encoded")
    request("GET", run_path + "/approval-gates", token=outsider_token, expected=403)
    state_path = run_path + "/checklists/0/item/0/state"
    request("PUT", state_path, {"new_state": "closed"}, admin_token, 409)
    request("PUT", run_path + "/finish", token=admin_token, expected=409)
    unchanged, _ = request("GET", run_path, token=admin_token)
    check(unchanged["checklists"][0]["items"][0]["state"] == "" and unchanged["current_status"] == "InProgress",
          "pending gate leaves dependent work open and prevents finishing the run")

    projection = {**projection, "source_version": 2, "assignee_user_id": reviewer["id"]}
    relay("POST", gates_path, projection)
    views, _ = request("GET", run_path + "/approval-gates", token=reviewer_token)
    check(views[0]["can_review"], "eligible assigned reviewer receives Review capability")
    relay("POST", gates_path, {**projection, "status": "approved"}, expected=409)
    check(True, "a conflicting status at the same source version is rejected")

    request("POST", "/api/v4/plugins/playbooks/disable", token=admin_token)
    request("POST", "/api/v4/plugins/playbooks/enable", token=admin_token)
    check(relay("POST", "/runs", creation) == created, "provisioning survives a real plugin disable/enable")
    views, _ = request("GET", run_path + "/approval-gates", token=reviewer_token)
    check(views[0]["status"] == "pending" and views[0]["can_review"], "gate projection survives plugin reactivation")

    subprocess.run(["docker", "restart", CONTAINER], check=True, stdout=subprocess.DEVNULL)
    wait_for_host()
    # The health endpoint is ready before plugins have finished activating.
    for attempt in range(60):
        try:
            relay("GET", "/capabilities")
            break
        except (AssertionError, urllib.error.URLError, ConnectionError):
            if attempt == 59:
                raise
            time.sleep(1)
    check(relay("POST", "/runs", creation) == created, "provisioning survives actual Mattermost process restart")
    check(relay("GET", by_key) == created, "operation-key recovery survives host restart")
    recovered_run, _ = request("GET", run_path, token=admin_token)
    recovered_conditions, _ = request("GET", run_path + "/conditions", token=admin_token)
    check(recovered_run["property_fields"] == run["property_fields"] and recovered_conditions == run_conditions,
          "property and condition snapshots survive restart without copying later template changes")
    matching_runs, _ = request("GET", API + "/runs?" + urllib.parse.urlencode({
        "team_id": team["id"], "playbook_id": playbook["id"], "per_page": 100,
    }), token=admin_token)
    check(matching_runs["total_count"] == 1, "retries and restarts leave exactly one run")
    channels, _ = request("GET", f'/api/v4/users/{admin["id"]}/teams/{team["id"]}/channels', token=admin_token)
    check(sum(channel["display_name"] == creation["name"] for channel in channels) == 1,
          "retries and restarts leave exactly one run channel")
    stale_result = relay("POST", gates_path, {**projection, "source_version": 1, "status": "cancelled"})
    check(stale_result["source_version"] == 2 and stale_result["status"] == "pending",
          "older gate versions cannot overwrite the saved projection after restart")

    projection = {**projection, "source_version": 3, "status": "approved"}
    relay("POST", gates_path, projection, actor=reviewer["id"])
    approved, _ = request("GET", run_path + "/approval-gates", token=reviewer_token)
    check(approved[0]["status"] == "approved" and not approved[0]["can_review"], "approved projection changes the external gate status")
    unchanged, _ = request("GET", run_path, token=admin_token)
    check(unchanged["current_status"] == "InProgress" and unchanged["checklists"][0]["items"][0]["state"] == "",
          "approval does not finish the run or complete its checklist task")
    request("PUT", state_path, {"new_state": "closed"}, admin_token)
    finished_task, _ = request("GET", run_path, token=admin_token)
    check(finished_task["checklists"][0]["items"][0]["state"] == "closed", "approved gate allows an explicit dependent-task completion")

    request("PUT", run_path + "/finish", token=admin_token)
    finished_run, _ = request("GET", run_path, token=admin_token)
    archived_channel, _ = request("GET", f'/api/v4/channels/{created["channel_id"]}', token=admin_token)
    check(finished_run["current_status"] == "Finished" and finished_run["end_at"] > 0 and
          archived_channel["delete_at"] > 0,
          "an explicit finish under approval finishes the run and archives its dedicated channel")
    projection = {**projection, "source_version": 4, "status": "pending"}
    revised = relay("POST", gates_path, projection, actor=reviewer["id"])
    check(revised == relay("POST", gates_path, projection, actor=reviewer["id"]),
          "a newer pending revision remains idempotent after an approved run was finished")
    reopened, _ = request("GET", run_path, token=admin_token)
    restored_channel, _ = request("GET", f'/api/v4/channels/{created["channel_id"]}', token=admin_token)
    check(reopened["current_status"] == "InProgress" and reopened["end_at"] == 0 and
          restored_channel["delete_at"] == 0,
          "a newer pending gate reopens the finished run and restores its auto-archived channel")
    check(reopened["checklists"][0]["items"][0]["state"] == "closed",
          "revising an approval retains the task completion history")
    request("PUT", run_path + "/finish", token=admin_token, expected=409)
    request("PUT", state_path, {"new_state": ""}, admin_token, 409)
    check(True, "the pending revision blocks finishing again and prevents dependent-task mutation")
    older_approval = relay("POST", gates_path, {**projection, "source_version": 3, "status": "approved"},
                           actor=reviewer["id"])
    check(older_approval["source_version"] == 4 and older_approval["status"] == "pending",
          "replaying the older approval cannot unblock the newer pending revision")
    check(relay("POST", "/runs", creation) == created and relay("GET", by_key) == created,
          "provisioning replay after approval revisions still returns the original run and channel")

    print(json.dumps({"host_image": image, "run_id": created["run_id"], "channel_id": created["channel_id"],
                      "reviewer_username": reviewer["username"],
                      "bundle_sha256": hashlib.sha256(bundle_data).hexdigest(), "result": "passed"}), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--caller-binary", type=Path, required=True)
    arguments = parser.parse_args()
    verify(arguments.bundle, arguments.caller_binary)
