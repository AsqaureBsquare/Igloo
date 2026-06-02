"""
EventBridge Lambda — triggered by ECS Task State Change events.
Keeps DynamoDB workspace status in sync with actual ECS task status.
Also extracts the EBS volume ID when a task reaches RUNNING state.
"""
import logging
from models import WorkspaceStatus
import db

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

ECS_TO_WORKSPACE = {
    "PROVISIONING":   WorkspaceStatus.CREATING,
    "PENDING":        WorkspaceStatus.CREATING,
    "ACTIVATING":     WorkspaceStatus.CREATING,
    "RUNNING":        WorkspaceStatus.RUNNING,
    "DEACTIVATING":   WorkspaceStatus.SLEEPING,
    "STOPPING":       WorkspaceStatus.SLEEPING,
    "DEPROVISIONING": WorkspaceStatus.SLEEPING,
    "STOPPED":        WorkspaceStatus.SLEEPING,
}


def _workspace_id_from_tags(tags: list) -> str | None:
    for tag in tags:
        if tag.get("key") == "igloo:workspace_id":
            return tag.get("value")
    return None


def _volume_id_from_attachments(attachments: list) -> str | None:
    """Extract EBS volume ID from ECS task attachments (populated when task is RUNNING)."""
    for attachment in attachments:
        if attachment.get("type") == "AmazonElasticBlockStorage":
            for detail in attachment.get("details", []):
                if detail.get("name") == "volumeId":
                    return detail.get("value")
    return None


def lambda_handler(event, context):
    detail      = event.get("detail", {})
    task_arn    = detail.get("taskArn", "")
    last_status = detail.get("lastStatus", "")
    tags        = detail.get("tags", [])
    attachments = detail.get("attachments", [])

    logger.info("Event detail keys: %s", list(detail.keys()))
    logger.info("Task: %s status: %s tags: %s", task_arn, last_status, tags)

    new_status = ECS_TO_WORKSPACE.get(last_status)
    if not new_status:
        logger.warning("Unknown ECS status %s for task %s", last_status, task_arn)
        return

    workspace_id = _workspace_id_from_tags(tags)
    if workspace_id:
        workspace = db.get_workspace(workspace_id)
    else:
        # Tags not propagated to EventBridge — look up by task_arn
        workspace = db.get_workspace_by_task_arn(task_arn)

    if not workspace:
        logger.info("No workspace found for task %s — not igloo-managed", task_arn)
        return

    if workspace.status in (WorkspaceStatus.DESTROYING,):
        return

    wid = workspace.workspace_id
    updates = {"status": new_status.value, "task_arn": task_arn}

    # When task reaches RUNNING, extract and store the EBS volume ID
    if new_status == WorkspaceStatus.RUNNING:
        volume_id = _volume_id_from_attachments(attachments)
        if volume_id:
            updates["volume_id"] = volume_id
            logger.info("Workspace %s: volume_id = %s", wid, volume_id)

    logger.info("Workspace %s: %s → %s", wid, workspace.status.value, new_status.value)
    db.update_workspace(wid, **updates)
