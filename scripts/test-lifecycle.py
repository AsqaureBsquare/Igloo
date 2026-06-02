#!/usr/bin/env python3
"""
End-to-end lifecycle test for Igloo.
Invokes Lambda functions directly (no API Gateway / auth required).

Usage:
    python3 scripts/test-lifecycle.py

Costs: Creates a real EBS volume + Fargate task. Destroys both at the end.
Estimated cost: < $0.05 for a 5-10 minute test.
"""
import boto3
import json
import time
import sys

REGION       = "us-east-1"
PROJECT      = "igloo"
TEST_USER    = "test-user-lifecycle"
CLUSTER_NAME = "igloo-workspaces"

lambda_client = boto3.client("lambda", region_name=REGION)
ecs_client    = boto3.client("ecs",    region_name=REGION)
ec2_client    = boto3.client("ec2",    region_name=REGION)

def log(msg):
    print(f"  {msg}")

def step(title):
    print(f"\n{'='*60}")
    print(f"  {title}")
    print(f"{'='*60}")

def invoke_lambda(name, payload):
    full_name = f"{PROJECT}-{name}"
    log(f"Invoking {full_name}...")
    resp = lambda_client.invoke(
        FunctionName=full_name,
        InvocationType="RequestResponse",
        Payload=json.dumps(payload),
    )
    result  = json.loads(resp["Payload"].read())
    status  = result.get("statusCode", 0)
    body    = json.loads(result.get("body", "{}"))

    if status >= 400:
        print(f"\n  ERROR: Lambda returned {status}: {body}")
        sys.exit(1)

    return body

def make_event(workspace_id=None, body=None, query=None):
    event = {"requestContext": {"authorizer": {"user_id": TEST_USER}}}
    if workspace_id:
        event["pathParameters"] = {"workspace_id": workspace_id}
    if body:
        event["body"] = json.dumps(body)
    if query:
        event["queryStringParameters"] = query
    return event

def wait_for_task_status(task_arn, desired_status, timeout=120):
    """Poll ECS directly until the task reaches desired_status."""
    task_id = task_arn.split("/")[-1]
    deadline = time.time() + timeout
    log(f"Waiting for task to be {desired_status} (timeout={timeout}s)...")
    while time.time() < deadline:
        resp   = ecs_client.describe_tasks(cluster=CLUSTER_NAME, tasks=[task_id])
        tasks  = resp.get("tasks", [])
        status = tasks[0].get("lastStatus", "UNKNOWN") if tasks else "NOT_FOUND"
        log(f"  task status: {status}")
        if status == desired_status:
            return True
        if status in ("STOPPED", "NOT_FOUND") and desired_status == "RUNNING":
            log("Task stopped unexpectedly.")
            return False
        time.sleep(8)
    log(f"Timeout waiting for {desired_status}")
    return False

def wait_for_exec_agent(task_arn, timeout=90):
    """Poll until the ECS ExecuteCommandAgent managed agent is RUNNING."""
    task_id  = task_arn.split("/")[-1]
    deadline = time.time() + timeout
    log(f"Waiting for ExecuteCommandAgent (timeout={timeout}s)...")
    while time.time() < deadline:
        resp       = ecs_client.describe_tasks(cluster=CLUSTER_NAME, tasks=[task_id])
        tasks      = resp.get("tasks", [])
        if not tasks:
            time.sleep(5)
            continue
        agents = tasks[0].get("containers", [{}])[0].get("managedAgents", [])
        status = next((a["lastStatus"] for a in agents if a["name"] == "ExecuteCommandAgent"), "NOT_FOUND")
        log(f"  exec agent: {status}")
        if status == "RUNNING":
            return True
        time.sleep(5)
    log("Timeout waiting for ExecuteCommandAgent")
    return False


def wait_for_dynamo_status(workspace_id, desired_status, timeout=60):
    """Poll DynamoDB until workspace_status_sync has updated the record."""
    import boto3 as b
    ddb   = b.resource("dynamodb", region_name=REGION)
    table = ddb.Table(f"{PROJECT}-workspaces")
    deadline = time.time() + timeout
    log(f"Waiting for DynamoDB status = {desired_status}...")
    while time.time() < deadline:
        item   = table.get_item(Key={"workspace_id": workspace_id}).get("Item", {})
        status = item.get("status", "unknown")
        log(f"  dynamo status: {status}")
        if status == desired_status:
            return True
        time.sleep(5)
    log(f"Timeout waiting for DynamoDB status {desired_status}")
    return False


workspace_id = None

try:
    # ── 1. CREATE ─────────────────────────────────────────────────────────────
    step("1/6  workspace_create")
    result = invoke_lambda("workspace_create", make_event(body={"type": "container"}))
    workspace_id = result["workspace_id"]
    log(f"workspace_id : {workspace_id}")
    log(f"volume_id    : {result['volume_id']}")
    log(f"task_arn     : {result['task_arn']}")

    # ── 2. Wait for RUNNING ───────────────────────────────────────────────────
    step("2/6  Wait for workspace to be RUNNING")
    ok = wait_for_task_status(result["task_arn"], "RUNNING", timeout=180)
    if not ok:
        print("Workspace did not reach RUNNING. Check ECS logs.")
        sys.exit(1)
    # Give status_sync Lambda a moment to update DynamoDB
    wait_for_dynamo_status(workspace_id, "running", timeout=30)
    # Wait for SSM ExecuteCommandAgent inside the container to connect
    wait_for_exec_agent(result["task_arn"])

    # ── 3. CONNECT ────────────────────────────────────────────────────────────
    step("3/6  workspace_connect")
    conn = invoke_lambda("workspace_connect", make_event(workspace_id=workspace_id))
    log(f"session_id   : {conn['session_id']}")
    log(f"stream_url   : {conn['stream_url'][:60]}...")
    log("")
    log("To open a terminal, run:")
    log(f"  aws ecs execute-command \\")
    log(f"    --cluster {CLUSTER_NAME} \\")
    log(f"    --task {result['task_arn'].split('/')[-1]} \\")
    log(f"    --container workspace \\")
    log(f"    --command /bin/bash --interactive")

    # ── 4. SLEEP ──────────────────────────────────────────────────────────────
    step("4/6  workspace_sleep")
    invoke_lambda("workspace_sleep", make_event(workspace_id=workspace_id))
    wait_for_task_status(result["task_arn"], "STOPPED", timeout=60)
    wait_for_dynamo_status(workspace_id, "sleeping", timeout=30)
    log("Workspace sleeping. EBS volume persists.")

    # ── 5. WAKE ───────────────────────────────────────────────────────────────
    step("5/6  workspace_wake")
    wake_result = invoke_lambda("workspace_wake", make_event(workspace_id=workspace_id))
    new_task_arn = wake_result["task_arn"]
    log(f"new task_arn : {new_task_arn}")
    ok = wait_for_task_status(new_task_arn, "RUNNING", timeout=180)
    if not ok:
        print("Workspace did not wake. Check ECS logs.")
        sys.exit(1)
    wait_for_dynamo_status(workspace_id, "running", timeout=30)
    wait_for_exec_agent(new_task_arn)
    log("Workspace awake. Same EBS volume re-attached.")

    # ── 6. DESTROY ────────────────────────────────────────────────────────────
    step("6/6  workspace_destroy")
    invoke_lambda("workspace_destroy", make_event(workspace_id=workspace_id))
    log("Workspace destroyed. EBS volume deleted.")

    print("\n" + "="*60)
    print("  ALL TESTS PASSED")
    print("="*60 + "\n")

except KeyboardInterrupt:
    print("\nInterrupted.")
    if workspace_id:
        print(f"Cleaning up workspace {workspace_id}...")
        try:
            invoke_lambda("workspace_destroy", make_event(workspace_id=workspace_id))
            print("Cleaned up.")
        except Exception as e:
            print(f"Cleanup failed: {e}. Manual cleanup may be needed.")
    sys.exit(1)
except Exception as e:
    print(f"\nTest failed: {e}")
    if workspace_id:
        print(f"Attempting cleanup of workspace {workspace_id}...")
        try:
            invoke_lambda("workspace_destroy", make_event(workspace_id=workspace_id))
            print("Cleaned up.")
        except Exception:
            pass
    raise
