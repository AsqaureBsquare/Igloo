import json
import logging
from models import WorkspaceStatus
from providers.aws import AWSProvider
import db

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

provider = AWSProvider()


def lambda_handler(event, context):
    try:
        workspace_id = event["pathParameters"]["workspace_id"]
        user_id      = event["requestContext"]["authorizer"]["user_id"]

        workspace = db.get_workspace(workspace_id)
        if not workspace:
            return {"statusCode": 404, "body": json.dumps({"error": "workspace not found"})}

        if workspace.user_id != user_id:
            return {"statusCode": 403, "body": json.dumps({"error": "forbidden"})}

        if workspace.status not in (WorkspaceStatus.SLEEPING, WorkspaceStatus.ERROR):
            return {
                "statusCode": 409,
                "body": json.dumps({"error": f"workspace is {workspace.status.value}, cannot wake"}),
            }

        db.update_workspace(workspace_id, status=WorkspaceStatus.WAKING.value)

        new_task_arn = provider.wake_workspace(workspace)

        db.update_workspace(
            workspace_id,
            status   = WorkspaceStatus.RUNNING.value,
            task_arn = new_task_arn,
        )

        logger.info("Woke workspace %s, new task %s", workspace_id, new_task_arn)
        return {
            "statusCode": 200,
            "body": json.dumps({
                "workspace_id": workspace_id,
                "status":       "running",
                "task_arn":     new_task_arn,
            }),
        }

    except Exception as e:
        logger.exception("workspace_wake failed")
        db.update_workspace(workspace_id, status=WorkspaceStatus.ERROR.value)
        return {"statusCode": 500, "body": json.dumps({"error": "internal error"})}
