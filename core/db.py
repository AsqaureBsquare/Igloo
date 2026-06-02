import os
import boto3
from datetime import datetime, timezone
from typing import Optional, List
from boto3.dynamodb.conditions import Key, Attr
from models import Workspace, WorkspaceStatus

TABLE_NAME = os.environ["WORKSPACES_TABLE"]

_table = None


def _get_table():
    global _table
    if _table is None:
        _table = boto3.resource("dynamodb").Table(TABLE_NAME)
    return _table


def put_workspace(workspace: Workspace) -> None:
    _get_table().put_item(Item=workspace.to_dynamo())


def get_workspace(workspace_id: str) -> Optional[Workspace]:
    resp = _get_table().get_item(Key={"workspace_id": workspace_id})
    item = resp.get("Item")
    return Workspace.from_dynamo(item) if item else None


def update_workspace(workspace_id: str, **fields) -> None:
    """Update arbitrary fields on a workspace. Always bumps last_activity."""
    fields["last_activity"] = datetime.now(timezone.utc).isoformat()

    set_parts = []
    names = {}
    values = {}

    for i, (k, v) in enumerate(fields.items()):
        placeholder = f"#f{i}"
        value_key   = f":v{i}"
        names[placeholder] = k
        values[value_key]  = v
        set_parts.append(f"{placeholder} = {value_key}")

    _get_table().update_item(
        Key={"workspace_id": workspace_id},
        UpdateExpression="SET " + ", ".join(set_parts),
        ExpressionAttributeNames=names,
        ExpressionAttributeValues=values,
    )


def delete_workspace(workspace_id: str) -> None:
    _get_table().delete_item(Key={"workspace_id": workspace_id})


def list_by_user(user_id: str) -> List[Workspace]:
    resp = _get_table().query(
        IndexName="user_id-index",
        KeyConditionExpression=Key("user_id").eq(user_id),
    )
    return [Workspace.from_dynamo(item) for item in resp.get("Items", [])]


def get_workspace_by_task_arn(task_arn: str) -> Optional[Workspace]:
    """Scan for a workspace by its current task ARN. Used by status_sync."""
    resp = _get_table().scan(
        FilterExpression=Attr("task_arn").eq(task_arn)
    )
    items = resp.get("Items", [])
    return Workspace.from_dynamo(items[0]) if items else None


def list_running() -> List[Workspace]:
    """Full table scan filtered to running workspaces. Fine for v1 scale."""
    resp = _get_table().scan(
        FilterExpression=Attr("status").eq(WorkspaceStatus.RUNNING.value)
    )
    return [Workspace.from_dynamo(item) for item in resp.get("Items", [])]
